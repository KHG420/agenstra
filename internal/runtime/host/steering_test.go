package host

import (
	"context"
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func TestSteeringWaitsForActiveWriteAndSupersedesUnsentCalls(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {Name: "records.get", Version: "1", InputSchema: agentcontract.JSON{"type": "object"}, Effect: "write", Replay: "never"}}, hook: func(ctx context.Context, _ string, _ agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		close(started)
		select {
		case <-release:
			return agentcontract.CapabilityResult{Data: agentcontract.JSON{"written": true}}, nil
		case <-ctx.Done():
			return agentcontract.CapabilityResult{}, ctx.Err()
		}
	}}
	decision := callDecision("records.get")
	second := decision.Calls[0]
	second.CallRef = "lookup-2"
	decision.Calls = append(decision.Calls, second)
	m := &hostModel{decisions: []agentcontract.Decision{decision}, hook: func(packet agentcontract.ContextPacket) {
		if packet.RoundIndex > 0 && (len(packet.Followups) != 1 || packet.Followups[0] != "steering: Stop after the first write") {
			t.Error("steering missing at next model decision", packet.Followups)
		}
	}}
	h := testHost(t, testStore(t), p, m)
	run := createTestHostRun(t, h)
	type result struct {
		run agentcontract.StoredRun
		err error
	}
	done := make(chan result, 1)
	go func() { r, err := h.Drive(t.Context(), run.RunID, "alice"); done <- result{r, err} }()
	<-started
	current, callErr := h.Store.GetRun(run.RunID, "alice")
	if callErr != nil {
		t.Error(callErr)
	}
	requestID := agentcontract.NewID()
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
	state, callErr2 := h.Restore(r.run)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if len(state.Followups) != 1 || len(state.Facts) != 1 || state.SteeringCursor == 0 {
		t.Fatalf("%+v", state)
	}
	invocation, callErr3 := h.Store.GetInvocation(run.RunID, reactcore.DeterministicInvocationID(run.RunID, "lookup-2"), "alice")
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
	h.Model = decisionModelFunc(func(ctx context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		calls++
		if calls == 1 {
			current, callErr4 := h.Store.GetRun(run.RunID, "alice")
			if callErr4 != nil {
				t.Error(callErr4)
			}
			if _, err := h.Steer(ctx, run.RunID, "alice", agentcontract.NewID(), "Answer in Chinese", current.Revision); err != nil {
				t.Fatal(err)
			}
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Old answer"}, nil
		}
		if len(packet.Followups) != 1 {
			t.Fatal("accepted steering was lost")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "新回答"}, nil
	})
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	state, callErr5 := h.Restore(run)
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
		payload := event["event"].(agentcontract.JSON)
		if payload["kind"] == "model_decided" && payload["decision_kind"] == "final" && payload["progress"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("progress event unavailable")
	}
}

func TestSteeringPersistsThroughRestartAndInvalidatesApproval(t *testing.T) {
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {Name: "records.get", Version: "1", InputSchema: agentcontract.JSON{}, Effect: "write", Replay: "never", ApprovalRequired: true}}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision("records.get")}})
	run := createTestHostRun(t, h)
	var callErr7 error
	run, callErr7 = h.Drive(t.Context(), run.RunID, "alice")
	if callErr7 != nil {
		t.Error(callErr7)
	}
	if run.Status != "needs_approval" {
		t.Fatal(run.Status)
	}
	state, callErr8 := h.Restore(run)
	if callErr8 != nil {
		t.Error(callErr8)
	}
	old := state.Pending[0]
	if _, err := h.Steer(t.Context(), run.RunID, "alice", agentcontract.NewID(), "Do not write; explain the capability", run.Revision); err != nil {
		t.Fatal(err)
	}
	h2 := testHost(t, h.Store, p, &hostModel{})
	run, err := h2.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 0 {
		t.Fatal(run.Status, err, p.calls)
	}
	if _, err = h2.Approve(t.Context(), run.RunID, "alice", old.InvocationID, old.ArgumentsSHA256, run.Revision, true); agentcontract.ErrorCode(err) != "approval_not_requested" {
		t.Fatal("old approval usable", err)
	}
}

func TestSteeringOwnershipRevisionIdempotencyAndCompletionFence(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	if _, err := h.Steer(t.Context(), run.RunID, "bob", agentcontract.NewID(), "change", run.Revision); !errors.Is(err, runstore.ErrRunNotFound) {
		t.Fatal(err)
	}
	if _, err := h.Steer(t.Context(), run.RunID, "alice", agentcontract.NewID(), "change", run.Revision+1); agentcontract.ErrorCode(err) != "revision_conflict" {
		t.Fatal(err)
	}
	id := agentcontract.NewID()
	if _, err := h.Steer(t.Context(), run.RunID, "alice", id, "change", run.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Steer(t.Context(), run.RunID, "alice", id, "different", run.Revision); agentcontract.ErrorCode(err) != "steering_request_conflict" {
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
	state, callErr10 := h.Restore(claimed)
	if callErr10 != nil {
		t.Error(callErr10)
	}
	state.Status = "completed"
	if _, err := h.save(claimed, state, "", nil, nil); !errors.Is(err, runstore.ErrSteeringPending) {
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

func TestSteeringQueueIsBoundedAndDoesNotBypassInput(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	for i := 0; i < 32; i++ {
		if _, err := h.Steer(t.Context(), run.RunID, "alice", agentcontract.NewID(), "Update the requested format", run.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Steer(t.Context(), run.RunID, "alice", agentcontract.NewID(), "Another update", run.Revision); agentcontract.ErrorCode(err) != "steering_queue_full" {
		t.Fatal(err)
	}
	h.Model = &hostModel{decisions: []agentcontract.Decision{{Kind: "request_input", Field: "destination", Prompt: "Where?"}}}
	other := createTestHostRun(t, h)
	var callErr12 error
	other, callErr12 = h.Drive(t.Context(), other.RunID, "alice")
	if callErr12 != nil {
		t.Error(callErr12)
	}
	if _, err := h.Steer(t.Context(), other.RunID, "alice", agentcontract.NewID(), "Shanghai", other.Revision); agentcontract.ErrorCode(err) != "steering_not_available" {
		t.Fatal("steering bypassed requested input contract", err)
	}
}
