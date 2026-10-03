package agenstra

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSteeringWaitsForActiveWriteAndSupersedesUnsentCalls(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := &hostProvider{caps: map[string]CapabilityDescription{"records.get": {Name: "records.get", Version: "1", InputSchema: JSON{"type": "object"}, Effect: "write", Replay: "never"}}, hook: func(ctx context.Context, _ string, _ JSON, _ *InvocationContext) (CapabilityResult, error) {
		close(started)
		select {
		case <-release:
			return CapabilityResult{Data: JSON{"written": true}}, nil
		case <-ctx.Done():
			return CapabilityResult{}, ctx.Err()
		}
	}}
	decision := callDecision("records.get")
	second := decision.Calls[0]
	second.CallRef = "lookup-2"
	decision.Calls = append(decision.Calls, second)
	m := &hostModel{decisions: []Decision{decision}, hook: func(packet ContextPacket) {
		if packet.RoundIndex > 0 && (len(packet.Followups) != 1 || packet.Followups[0] != "steering: Stop after the first write") {
			t.Error("steering missing at next model decision", packet.Followups)
		}
	}}
	h := testHost(t, testStore(t), p, m)
	run := createTestHostRun(t, h)
	type result struct {
		run StoredRun
		err error
	}
	done := make(chan result, 1)
	go func() { r, err := h.Drive(t.Context(), run.RunID, "alice"); done <- result{r, err} }()
	<-started
	current, callErr := h.Store.GetRun(run.RunID, "alice")
	if callErr != nil {
		t.Error(callErr)
	}
	requestID := NewID()
	if _, err := h.Steer(t.Context(), run.RunID, "alice", requestID, "Stop after the first write", current.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Steer(t.Context(), run.RunID, "alice", requestID, "Stop after the first write", current.Revision); err != nil {
		t.Fatal("duplicate request", err)
	}
	close(release)
	r := <-done
	if r.err != nil || r.run.Status != "completed" || p.calls != 1 {
		t.Fatalf("%+v calls=%d", r, p.calls)
	}
	state, callErr2 := h.restore(r.run)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if len(state.Followups) != 1 || len(state.Facts) != 1 || state.SteeringCursor == 0 {
		t.Fatalf("%+v", state)
	}
	invocation, callErr3 := h.Store.GetInvocation(run.RunID, deterministicInvocationID(run.RunID, "lookup-2"), "alice")
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if invocation["status"] != "failed" || invocation["error_code"] != "steering_superseded" {
		t.Fatal("unsent call was not superseded", invocation)
	}
}

func TestSteeringAcceptedDuringFinalReachesNextDecision(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	calls := 0
	h.Model = decisionModelFunc(func(ctx context.Context, packet ContextPacket, _ string) (Decision, error) {
		calls++
		if calls == 1 {
			current, callErr4 := h.Store.GetRun(run.RunID, "alice")
			if callErr4 != nil {
				t.Error(callErr4)
			}
			if _, err := h.Steer(ctx, run.RunID, "alice", NewID(), "Answer in Chinese", current.Revision); err != nil {
				t.Fatal(err)
			}
			return Decision{Kind: "final", AnswerMarkdown: "Old answer"}, nil
		}
		if len(packet.Followups) != 1 {
			t.Fatal("accepted steering was lost")
		}
		return Decision{Kind: "final", AnswerMarkdown: "新回答"}, nil
	})
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	state, callErr5 := h.restore(run)
	if callErr5 != nil {
		t.Error(callErr5)
	}
	if err != nil || calls != 2 || state.AnswerMarkdown != "新回答" {
		t.Fatalf("%+v %v calls=%d", state, err, calls)
	}
	events, callErr6 := h.Store.ListEvents(run.RunID, "alice", 0, 100)
	if callErr6 != nil {
		t.Error(callErr6)
	}
	found := false
	for _, event := range events {
		payload := event["event"].(JSON)
		if payload["kind"] == "model_decided" && payload["decision_kind"] == "final" && payload["progress"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("progress event unavailable")
	}
}

func TestSteeringPersistsThroughRestartAndInvalidatesApproval(t *testing.T) {
	p := &hostProvider{caps: map[string]CapabilityDescription{"records.get": {Name: "records.get", Version: "1", InputSchema: JSON{}, Effect: "write", Replay: "never", ApprovalRequired: true}}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision("records.get")}})
	run := createTestHostRun(t, h)
	var callErr7 error
	run, callErr7 = h.Drive(t.Context(), run.RunID, "alice")
	if callErr7 != nil {
		t.Error(callErr7)
	}
	if run.Status != "needs_approval" {
		t.Fatal(run.Status)
	}
	state, callErr8 := h.restore(run)
	if callErr8 != nil {
		t.Error(callErr8)
	}
	old := state.Pending[0]
	if _, err := h.Steer(t.Context(), run.RunID, "alice", NewID(), "Do not write; explain the capability", run.Revision); err != nil {
		t.Fatal(err)
	}
	h2 := testHost(t, h.Store, p, &hostModel{})
	run, err := h2.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 0 {
		t.Fatal(run.Status, err, p.calls)
	}
	if _, err = h2.Approve(t.Context(), run.RunID, "alice", old.InvocationID, old.ArgumentsSHA256, run.Revision, true); ErrorCode(err) != "approval_not_requested" {
		t.Fatal("old approval usable", err)
	}
}

func TestSteeringOwnershipRevisionIdempotencyAndCompletionFence(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	if _, err := h.Steer(t.Context(), run.RunID, "bob", NewID(), "change", run.Revision); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	if _, err := h.Steer(t.Context(), run.RunID, "alice", NewID(), "change", run.Revision+1); ErrorCode(err) != "revision_conflict" {
		t.Fatal(err)
	}
	id := NewID()
	if _, err := h.Steer(t.Context(), run.RunID, "alice", id, "change", run.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Steer(t.Context(), run.RunID, "alice", id, "different", run.Revision); ErrorCode(err) != "steering_request_conflict" {
		t.Fatal(err)
	}
	claimed, callErr9 := h.Store.Claim(run.RunID, "alice", 60)
	if callErr9 != nil {
		t.Error(callErr9)
	}
	defer func(release func(string, string, string) error, id, owner, token string) {
		if err := release(id, owner, token); err != nil {
			t.Error(err)
		}
	}(h.Store.Release, run.RunID, "alice", claimed.LeaseToken)
	state, callErr10 := h.restore(claimed)
	if callErr10 != nil {
		t.Error(callErr10)
	}
	state.Status = "completed"
	if _, err := h.save(claimed, state, "", nil, nil); !errors.Is(err, errSteeringPending) {
		t.Fatal("completion ignored accepted steering", err)
	}
	current, callErr11 := h.Store.GetRun(run.RunID, "alice")
	if callErr11 != nil {
		t.Error(callErr11)
	}
	if current.Status == "completed" {
		t.Fatal("completion committed")
	}
}

func TestSteeringHTTPContract(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	s := &HTTPServer{Host: h}
	body := fmtSteeringBody(t, NewID(), run.Revision)
	req := httptest.NewRequest("POST", "/runs/"+run.RunID+"/steer", strings.NewReader(body))
	out := httptest.NewRecorder()
	s.runHTTP(out, req, "alice")
	if out.Code != 202 || strings.Contains(out.Body.String(), "lease_token") {
		t.Fatal(out.Code, out.Body.String())
	}
}

func TestSteeringQueueIsBoundedAndDoesNotBypassInput(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	for i := 0; i < 32; i++ {
		if _, err := h.Steer(t.Context(), run.RunID, "alice", NewID(), "Update the requested format", run.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Steer(t.Context(), run.RunID, "alice", NewID(), "Another update", run.Revision); ErrorCode(err) != "steering_queue_full" {
		t.Fatal(err)
	}
	h.Model = &hostModel{decisions: []Decision{{Kind: "request_input", Field: "destination", Prompt: "Where?"}}}
	other := createTestHostRun(t, h)
	var callErr12 error
	other, callErr12 = h.Drive(t.Context(), other.RunID, "alice")
	if callErr12 != nil {
		t.Error(callErr12)
	}
	if _, err := h.Steer(t.Context(), other.RunID, "alice", NewID(), "Shanghai", other.Revision); ErrorCode(err) != "steering_not_available" {
		t.Fatal("steering bypassed requested input contract", err)
	}
}

func fmtSteeringBody(t *testing.T, id string, revision int) string {
	raw, callErr13 := CanonicalJSON(JSON{"request_id": id, "text": "Explain first", "revision": revision})
	if callErr13 != nil {
		t.Error(callErr13)
	}
	return string(raw)
}
