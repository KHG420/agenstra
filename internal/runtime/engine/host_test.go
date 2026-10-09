package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type hostModel struct {
	mu        sync.Mutex
	decisions []Decision
	calls     int
	hook      func(ContextPacket)
}

func (m *hostModel) Decide(ctx context.Context, packet ContextPacket, prompt string) (Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hook != nil {
		m.hook(packet)
	}
	m.calls++
	if len(m.decisions) == 0 {
		ids := []string{}
		for _, fact := range packet.Facts {
			ids = append(ids, fact.FactID)
		}
		return Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Done", FactIDs: ids}, nil
	}
	d := m.decisions[0]
	m.decisions = m.decisions[1:]
	return d, nil
}

type hostProvider struct {
	mu      sync.Mutex
	caps    map[string]CapabilityDescription
	calls   int
	hook    func(context.Context, string, map[string]any, *InvocationContext) (CapabilityResult, error)
	binding string
}

func (p *hostProvider) Capabilities() map[string]CapabilityDescription {
	if p.caps != nil {
		return p.caps
	}
	return map[string]CapabilityDescription{"records.get": {Name: "records.get", Version: "1", Description: "Look up record", InputSchema: JSON{"type": "object"}, Effect: "read", Replay: "safe", ReferenceScope: "durable", SkillsList: []string{}}}
}
func (p *hostProvider) Skills() map[string]Skill { return map[string]Skill{} }
func (p *hostProvider) SystemPrompt() string     { return "Use capabilities" }
func (p *hostProvider) BindingID() string        { return p.binding }
func (p *hostProvider) Close() error             { return nil }
func (p *hostProvider) Invoke(ctx context.Context, name string, args map[string]any, inv *InvocationContext) (CapabilityResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.hook != nil {
		return p.hook(ctx, name, args, inv)
	}
	return CapabilityResult{Data: JSON{"id": "R-1"}, ReferenceScope: "durable"}, nil
}
func testHost(t *testing.T, store *SQLiteStore, provider *hostProvider, model *hostModel) *AgentHost {
	t.Helper()
	return NewAgentHost(store, func(context.Context, string, string) (CapabilityProvider, error) { return provider, nil }, model, func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{GrantedCapabilities: map[string]bool{"records.get": true, "job.submit": true, "job.status": true}, AllowModelData: true}, nil
	})
}
func callDecision(name string) Decision {
	return Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "lookup-1", Capability: name, Arguments: JSON{"id": "R-1"}, Reason: "Look up record"}}}
}
func createTestHostRun(t *testing.T, h *AgentHost) StoredRun {
	t.Helper()
	r, e := h.Create(t.Context(), "alice", "records", "Look up R-1", "")
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestHostRefreshesReadAfterEachWrite(t *testing.T) {
	version, reads, writes := 0, 0, 0
	p := &hostProvider{caps: map[string]CapabilityDescription{
		"records.get":    {Name: "records.get", Effect: "read", Replay: "safe", InputSchema: JSON{"type": "object"}},
		"records.update": {Name: "records.update", Effect: "write", Replay: "never", InputSchema: JSON{"type": "object"}},
	}, hook: func(_ context.Context, name string, _ JSON, _ *InvocationContext) (CapabilityResult, error) {
		if name == "records.update" {
			writes++
			version++
		} else {
			reads++
		}
		return CapabilityResult{Data: JSON{"version": version}}, nil
	}}
	m := &hostModel{}
	for i := 0; i < 3; i++ {
		read := callDecision("records.get")
		read.Calls[0].CallRef = fmt.Sprintf("read-%d", i)
		m.decisions = append(m.decisions, read)
		if i < 2 {
			write := callDecision("records.update")
			write.Calls[0].CallRef = fmt.Sprintf("write-%d", i)
			write.Calls[0].Arguments["value"] = i + 1
			m.decisions = append(m.decisions, write)
		}
	}
	h := NewAgentHost(testStore(t), func(context.Context, string, string) (CapabilityProvider, error) { return p, nil }, m,
		func(context.Context, string, string) (ExecutionPolicy, error) {
			return ExecutionPolicy{GrantedCapabilities: map[string]bool{"records.get": true, "records.update": true}, AllowModelData: true}, nil
		})
	r := createTestHostRun(t, h)
	r, err := h.Drive(t.Context(), r.RunID, "alice")
	if err != nil || r.Status != "completed" || reads != 3 || writes != 2 {
		t.Fatalf("refresh: status=%s reads=%d writes=%d err=%v", r.Status, reads, writes, err)
	}
	state, err := h.restore(r)
	if err != nil {
		t.Fatal(err)
	}
	latest := state.Facts[len(state.Facts)-1]
	if latest.SourceCapability != "records.get" || fmt.Sprint(latest.Value["data"].(map[string]any)["version"]) != "2" {
		t.Fatal("latest read did not observe the second write", latest)
	}
}
func TestHostValidatesNestedInputBeforeApproval(t *testing.T) {
	cap := CapabilityDescription{Name: "records.get", Effect: "destructive", Replay: "never", ApprovalRequired: true, InputSchema: JSON{
		"type": "object", "additionalProperties": false,
		"properties": JSON{"operation": JSON{"type": "string"}, "arguments": JSON{"type": "object"}},
		"anyOf": []any{JSON{"properties": JSON{"operation": JSON{"const": "create"}, "arguments": JSON{
			"type": "object", "properties": JSON{"request": JSON{"type": "object", "properties": JSON{"title": JSON{"type": "string"}}, "required": []any{"title"}, "additionalProperties": false}},
			"required": []any{"request"}, "additionalProperties": false,
		}}, "required": []any{"operation", "arguments"}}},
	}}
	p := &hostProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}
	m := &hostModel{}
	for i, args := range []JSON{
		{"operation": "create", "request": JSON{"title": "Test"}},
		{"operation": "create", "arguments": JSON{"request": JSON{"title": 7}}},
		{"operation": "create", "arguments": JSON{"request": JSON{"title": "Test"}}},
	} {
		m.decisions = append(m.decisions, Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: fmt.Sprintf("create-%d", i), Capability: cap.Name, Arguments: args, Reason: "Create a record"}}})
	}
	h := testHost(t, testStore(t), p, m)
	r := createTestHostRun(t, h)
	r, err := h.Drive(t.Context(), r.RunID, "alice")
	if err != nil || r.Status != "needs_approval" {
		t.Fatal(r.Status, err)
	}
	s, err := h.restore(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Observations) != 2 || p.calls != 0 {
		t.Fatalf("invalid calls reached approval or execution: observations=%+v calls=%d", s.Observations, p.calls)
	}
	for _, observation := range s.Observations {
		if observation.ErrorCode == nil || *observation.ErrorCode != "capability_input_invalid" {
			t.Fatal(observation)
		}
	}
	item := s.Pending[0]
	if item.Call.CallRef != "create-2" {
		t.Fatal("approval must cover the corrected call", item)
	}
	if _, err = h.Approve(t.Context(), r.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, r.Revision, true); err != nil {
		t.Fatal(err)
	}
	r, err = h.Drive(t.Context(), r.RunID, "alice")
	if err != nil || r.Status != "completed" || p.calls != 1 {
		t.Fatal(r.Status, p.calls, err)
	}
}
func TestHostDurableRunAndInput(t *testing.T) {
	s := testStore(t)
	p := &hostProvider{}
	m := &hostModel{decisions: []Decision{callDecision("records.get"), {Schema: "agenstra.decision.v1", Kind: "request_input", Field: "destination", Prompt: "Where?"}}}
	h := testHost(t, s, p, m)
	r := createTestHostRun(t, h)
	r, e := h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "needs_input" {
		t.Fatalf("drive: %+v %v", r, e)
	}
	state, e := h.restore(r)
	if e != nil || len(state.Facts) != 1 {
		t.Fatalf("facts: %+v %v", state, e)
	}
	if _, e = h.SupplyInput(t.Context(), r.RunID, "bob", "destination", "Shanghai", r.Revision); !errors.Is(e, ErrRunNotFound) {
		t.Fatalf("owner input: %v", e)
	}
	if _, e = h.SupplyInput(t.Context(), r.RunID, "alice", "destination", "Shanghai", r.Revision+1); ErrorCode(e) != "revision_conflict" {
		t.Fatalf("revision: %v", e)
	}
	h = testHost(t, s, p, &hostModel{hook: func(c ContextPacket) {
		if len(c.Facts) != 1 || len(c.Followups) != 1 || !strings.Contains(c.Followups[0], "Shanghai") {
			t.Errorf("restored context: %+v", c)
		}
	}})
	r, e = h.SupplyInput(t.Context(), r.RunID, "alice", "destination", "Shanghai", r.Revision)
	if e != nil {
		t.Fatal(e)
	}
	r, e = h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "completed" || p.calls != 1 {
		t.Fatalf("resume: %+v %v calls=%d", r, e, p.calls)
	}
}
func TestHostRequestIDCompatibility(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	r, e := h.Create(t.Context(), "alice", "records", "task", "request-1")
	if e != nil {
		t.Fatal(e)
	}
	again, e := h.Create(t.Context(), "alice", "records", "task", "request-1")
	if e != nil || again.RunID != r.RunID {
		t.Fatalf("repeat: %v", e)
	}
	if _, e = h.Create(t.Context(), "alice", "records", "other", "request-1"); ErrorCode(e) != "request_id_conflict" {
		t.Fatalf("conflict: %v", e)
	}
	other, e := h.Create(t.Context(), "alice\u0000request-1", "records", "task", "request")
	if e != nil || other.RunID == r.RunID {
		t.Fatalf("collision: %v", e)
	}
	// Python uuid.uuid5(NAMESPACE_URL, json.dumps(["alice","request-1"], separators=(",",":"))).
	if r.RunID != "007995cc-233b-5505-b320-267746e26a16" {
		t.Fatalf("Python request UUID mismatch: %s", r.RunID)
	}
}
func TestHostApprovalBindsArgumentsPolicyRevisionExpiry(t *testing.T) {
	s := testStore(t)
	p := &hostProvider{caps: map[string]CapabilityDescription{"records.get": {Name: "records.get", Version: "1", Description: "Compute", InputSchema: JSON{"type": "object"}, Effect: "compute", Replay: "never", ReferenceScope: "durable", ApprovalRequired: true}}}
	h := testHost(t, s, p, &hostModel{decisions: []Decision{callDecision("records.get")}})
	now := unixNow()
	h.Clock = func() float64 { return now }
	r := createTestHostRun(t, h)
	r, e := h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "needs_approval" || p.calls != 0 {
		t.Fatalf("approval: %+v %v", r, e)
	}
	state, callErr := h.restore(r)
	if callErr != nil {
		t.Error(callErr)
	}
	item := state.Pending[0]
	for _, tc := range []struct {
		owner, hash string
		revision    int
		want        string
	}{{"bob", item.ArgumentsSHA256, r.Revision, "run not found"}, {"alice", strings.Repeat("0", 64), r.Revision, "approval_arguments_changed"}, {"alice", item.ArgumentsSHA256, r.Revision + 1, "revision_conflict"}} {
		_, e = h.Approve(t.Context(), r.RunID, tc.owner, item.InvocationID, tc.hash, tc.revision, true)
		if ErrorCode(e) != tc.want {
			t.Fatalf("approve want %s: %v", tc.want, e)
		}
	}
	r, e = h.Approve(t.Context(), r.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, r.Revision, true)
	if e != nil {
		t.Fatal(e)
	}
	h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{AllowModelData: true}, nil
	}
	r, e = h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "needs_authorization" || p.calls != 0 {
		t.Fatalf("revocation: %+v %v calls=%d", r, e, p.calls)
	}
	h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{AllowModelData: true, GrantedCapabilities: map[string]bool{"records.get": true}}, nil
	}
	now += h.Settings.ApprovalSeconds + 1
	r, e = h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "needs_approval" || p.calls != 0 {
		t.Fatalf("expired approval: %+v %v", r, e)
	}
}
func TestHostUncertainCommitReplay(t *testing.T) {
	for _, replay := range []string{"never", "idempotent", "safe"} {
		t.Run(replay, func(t *testing.T) {
			s := testStore(t)
			seen := []string{}
			p := &hostProvider{caps: map[string]CapabilityDescription{"records.get": {Name: "records.get", Version: "1", InputSchema: JSON{"type": "object"}, Effect: "compute", Replay: replay, ReferenceScope: "durable"}}}
			p.hook = func(ctx context.Context, name string, args map[string]any, inv *InvocationContext) (CapabilityResult, error) {
				seen = append(seen, inv.IdempotencyKey)
				if len(seen) == 1 {
					return CapabilityResult{}, errors.New("response lost after external commit")
				}
				return CapabilityResult{Data: JSON{"id": "R-1"}}, nil
			}
			h := testHost(t, s, p, &hostModel{decisions: []Decision{callDecision("records.get")}})
			now := unixNow()
			h.Clock = func() float64 { return now }
			r := createTestHostRun(t, h)
			r, e := h.Drive(t.Context(), r.RunID, "alice")
			if e != nil {
				t.Fatal(e)
			}
			if replay == "never" {
				if r.Status != "needs_reconciliation" || len(seen) != 1 {
					t.Fatalf("unsafe replay: %+v", r)
				}
				return
			}
			if r.Status != "waiting" {
				t.Fatalf("retry wait: %+v", r)
			}
			now += 10
			r, e = h.Drive(t.Context(), r.RunID, "alice")
			if e != nil || r.Status != "completed" || len(seen) != 2 || seen[0] != seen[1] {
				t.Fatalf("replay: %+v %v %v", r, e, seen)
			}
		})
	}
}

func TestHostRecoversPreparedJournalAfterLeaseExpiry(t *testing.T) {
	for _, replay := range []string{"never", "idempotent"} {
		t.Run(replay, func(t *testing.T) {
			s := testStore(t)
			now := 1000.
			s.Clock = func() float64 { return now }
			cap := CapabilityDescription{Name: "records.get", Version: "1", InputSchema: JSON{"type": "object"}, Effect: "compute", Replay: replay, ReferenceScope: "durable", IdempotencyArgument: []string{"idempotency_key"}}
			p := &hostProvider{caps: map[string]CapabilityDescription{"records.get": cap}}
			m := &hostModel{decisions: []Decision{callDecision("records.get")}}
			h := testHost(t, s, p, m)
			h.Clock = func() float64 { return now }
			r := createTestHostRun(t, h)
			r, e := s.Claim(r.RunID, "alice", 60)
			if e != nil {
				t.Fatal(e)
			}
			state, e := h.restore(r)
			if e != nil {
				t.Fatal(e)
			}
			runtime := &AgentRuntime{Provider: p, Model: m, Grants: map[string]bool{"records.get": true}}
			if e = runtime.Step(t.Context(), state, nil); e != nil {
				t.Fatal(e)
			}
			item := &state.Pending[0]
			inv := invocationContext(r, item, runtime.ConnectionID)
			normalized, e := BindIdempotency(item.Call, cap, inv)
			if e != nil {
				t.Fatal(e)
			}
			item.Call = normalized
			item.ArgumentsSHA256 = ArgumentsDigest(normalized)
			item.Attempts = 1
			item.Status = "in_flight"
			expectedID := item.InvocationID
			r, e = h.save(r, state, fingerprint(p), nil, map[string]any{"kind": "call_started"})
			if e != nil {
				t.Fatal(e)
			}
			// Simulate a worker exiting after the upstream commit and before checkpointing its response.
			now = 1061
			p.hook = func(ctx context.Context, name string, args map[string]any, inv *InvocationContext) (CapabilityResult, error) {
				if inv.IdempotencyKey != expectedID || args["idempotency_key"] != expectedID {
					t.Errorf("recovered key changed: %v %+v", args, inv)
				}
				return CapabilityResult{Data: JSON{"id": "R-1"}}, nil
			}
			r, e = h.Drive(t.Context(), r.RunID, "alice")
			if e != nil {
				t.Fatal(e)
			}
			wantStatus := "completed"
			wantCalls := 1
			if replay == "never" {
				wantStatus = "needs_reconciliation"
				wantCalls = 0
			}
			if r.Status != wantStatus || p.calls != wantCalls {
				t.Fatalf("recovery: %s calls=%d", r.Status, p.calls)
			}
		})
	}
}
func TestHostJobPollingSurvivesRestart(t *testing.T) {
	binding := &OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "job.status", PollArgument: []string{"id"}, PendingStates: []string{"queued", "running"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}, IntervalSeconds: 1, TimeoutSeconds: 3600}
	p := &hostProvider{caps: map[string]CapabilityDescription{"job.submit": {Name: "job.submit", Version: "1", Description: "Submit", InputSchema: JSON{"type": "object"}, Effect: "compute", Replay: "idempotent", ReferenceScope: "durable", Operation: binding}, "job.status": {Name: "job.status", Version: "1", Description: "Status", InputSchema: JSON{"type": "object"}, Effect: "read", Replay: "safe", ReferenceScope: "durable"}}}
	p.hook = func(ctx context.Context, name string, args map[string]any, inv *InvocationContext) (CapabilityResult, error) {
		status := "queued"
		if name == "job.status" {
			status = "succeeded"
			if args["id"] != "job-1" {
				t.Errorf("poll args: %v", args)
			}
		}
		return CapabilityResult{Data: JSON{"id": "job-1", "status": status}}, nil
	}
	s := testStore(t)
	m := &hostModel{decisions: []Decision{callDecision("job.submit")}}
	h := testHost(t, s, p, m)
	now := unixNow()
	h.Clock = func() float64 { return now }
	r := createTestHostRun(t, h)
	r, e := h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "waiting" || m.calls != 1 {
		t.Fatalf("job: %+v %v", r, e)
	}
	state, callErr2 := h.restore(r)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	firstFact := state.Facts[0].FactID
	h = testHost(t, s, p, m)
	now += 2
	h.Clock = func() float64 { return now }
	r, e = h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "completed" || m.calls != 2 || p.calls != 2 {
		t.Fatalf("poll: %+v %v", r, e)
	}
	var callErr3 error
	state, callErr3 = h.restore(r)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if len(state.Facts) != 1 || state.Facts[0].FactID == firstFact || state.PollCallsUsed != 1 {
		t.Fatalf("latest status: %+v", state)
	}
	if _, e = s.GetArtifact(r.RunID, firstFact, "alice"); e != nil {
		t.Fatalf("old receipt lost: %v", e)
	}
}
func TestHostConcurrentDriversAndCancellation(t *testing.T) {
	p := &hostProvider{}
	entered := make(chan struct{})
	p.hook = func(ctx context.Context, name string, args map[string]any, inv *InvocationContext) (CapabilityResult, error) {
		close(entered)
		<-ctx.Done()
		return CapabilityResult{}, ctx.Err()
	}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision("records.get")}})
	h.Settings.LeaseSeconds = 3
	r := createTestHostRun(t, h)
	result := make(chan StoredRun, 1)
	failure := make(chan error, 1)
	go func() { v, e := h.Drive(t.Context(), r.RunID, "alice"); result <- v; failure <- e }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("provider not called")
	}
	if _, e := h.Drive(t.Context(), r.RunID, "alice"); !errors.Is(e, ErrStoreConflict) {
		t.Fatalf("competing driver: %v", e)
	}
	if _, e := h.Cancel(t.Context(), r.RunID, "alice"); e != nil {
		t.Fatal(e)
	}
	select {
	case got := <-result:
		if e := <-failure; e != nil || got.Status != "cancelled" {
			t.Fatalf("cancel: %+v %v", got, e)
		}
		state, callErr4 := h.restore(got)
		if callErr4 != nil {
			t.Error(callErr4)
		}
		if state.Pending[0].Status != "unknown" || p.calls != 1 {
			t.Fatalf("cancel outcome: %+v calls=%d", state, p.calls)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancel did not interrupt")
	}
}

func TestHostCancellationInterruptsModelBeforeLongLeaseHeartbeat(t *testing.T) {
	entered := make(chan struct{})
	model := decisionModelFunc(func(ctx context.Context, _ ContextPacket, _ string) (Decision, error) {
		close(entered)
		<-ctx.Done()
		return Decision{}, ctx.Err()
	})
	provider := &hostProvider{}
	h := testHost(t, testStore(t), provider, &hostModel{})
	h.Model = model
	h.Settings.LeaseSeconds = 90
	run := createTestHostRun(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		result, err := h.Drive(ctx, run.RunID, "alice")
		if err == nil && result.Status != "cancelled" {
			err = fmt.Errorf("unexpected cancellation status: %s", result.Status)
		}
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("model request did not start")
	}
	if _, err := h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
		if provider.calls != 0 {
			t.Fatal("cancellation invoked a business provider")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("model cancellation waited for the 30-second lease heartbeat")
	}
}

func TestHostStaleDriverCannotAdoptReplacementLease(t *testing.T) {
	for _, branch := range []string{"cancellation", "state-budget"} {
		t.Run(branch, func(t *testing.T) {
			s := testStore(t)
			var clock atomic.Int64
			clock.Store(1000)
			s.Clock = func() float64 { return float64(clock.Load()) }
			entered := make(chan struct{})
			release := make(chan struct{})
			p := &hostProvider{}
			m := &hostModel{decisions: []Decision{callDecision("records.get")}}
			if branch == "cancellation" {
				p.hook = func(ctx context.Context, name string, args map[string]any, inv *InvocationContext) (CapabilityResult, error) {
					close(entered)
					<-ctx.Done()
					return CapabilityResult{}, ctx.Err()
				}
			} else {
				m.decisions[0].Calls[0].Arguments = JSON{"large": strings.Repeat("x", 3000)}
				m.hook = func(ContextPacket) { close(entered); <-release }
			}
			h := testHost(t, s, p, m)
			h.Clock = s.Clock
			if branch == "state-budget" {
				// Allow the request telemetry checkpoint, then exceed the budget
				// with the model's 3000-byte decision after the lease is replaced.
				h.Settings.MaxStateBytes = 4096
			}
			r := createTestHostRun(t, h)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := make(chan error, 1)
			go func() { _, err := h.Drive(ctx, r.RunID, "alice"); finished <- err }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("driver did not enter request")
			}
			clock.Store(1061)
			replacement, err := s.Claim(r.RunID, "alice", 60)
			if err != nil {
				t.Fatal(err)
			}
			if branch == "cancellation" {
				if _, err = s.RequestCancel(r.RunID, "alice"); err != nil {
					t.Fatal(err)
				}
				cancel()
			} else {
				close(release)
			}
			select {
			case err = <-finished:
				if !errors.Is(err, ErrLeaseLost) {
					t.Errorf("stale driver should lose lease, got %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("driver failed to finish")
			}
			current, err := s.GetRun(r.RunID, "alice")
			if err != nil {
				t.Fatal(err)
			}
			if current.Revision != replacement.Revision || current.Status != replacement.Status || current.LeaseToken != replacement.LeaseToken {
				t.Fatalf("replacement lease/state overwritten: before revision=%d status=%s after revision=%d status=%s", replacement.Revision, replacement.Status, current.Revision, current.Status)
			}
		})
	}
}
func TestHostModelDataPolicyAndFingerprint(t *testing.T) {
	p := &hostProvider{}
	m := &hostModel{}
	h := testHost(t, testStore(t), p, m)
	h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) { return ExecutionPolicy{}, nil }
	if _, e := h.Create(t.Context(), "alice", "records", "task", ""); ErrorCode(e) != "model_data_not_authorized" || m.calls != 0 {
		t.Fatalf("data denied: %v", e)
	}
	h = testHost(t, h.Store, p, &hostModel{decisions: []Decision{{Schema: "agenstra.decision.v1", Kind: "request_input", Field: "name", Prompt: "Which?"}}})
	r := createTestHostRun(t, h)
	r, e := h.Drive(t.Context(), r.RunID, "alice")
	if e != nil {
		t.Fatal(e)
	}
	r, e = h.SupplyInput(t.Context(), r.RunID, "alice", "name", "R-1", r.Revision)
	if e != nil {
		t.Fatal(e)
	}
	p.binding = "changed-principal"
	r, e = h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "failed" {
		t.Fatalf("pack change: %+v %v", r, e)
	}
	state, callErr5 := h.restore(r)
	if callErr5 != nil {
		t.Error(callErr5)
	}
	if state.ErrorCode == nil || *state.ErrorCode != "pack_changed" {
		t.Fatalf("change code: %+v", state)
	}
}
func TestHostCallJournalPrecedesIO(t *testing.T) {
	s := testStore(t)
	p := &hostProvider{}
	p.hook = func(ctx context.Context, name string, args map[string]any, inv *InvocationContext) (CapabilityResult, error) {
		stored, e := s.GetInvocation(inv.RunID, inv.InvocationID, inv.OwnerID)
		if e != nil || stored["status"] != "in_flight" || stored["arguments_sha256"] == "" {
			t.Errorf("journal before IO: %v %v", stored, e)
		}
		return CapabilityResult{Data: JSON{"ok": true}}, nil
	}
	h := testHost(t, s, p, &hostModel{decisions: []Decision{callDecision("records.get")}})
	r := createTestHostRun(t, h)
	r, e := h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "completed" {
		t.Fatalf("run: %+v %v", r, e)
	}
}
func TestHostStateBudgetStopsBeforeExternalSend(t *testing.T) {
	s := testStore(t)
	p := &hostProvider{}
	d := callDecision("records.get")
	d.Calls[0].Arguments = JSON{"large": strings.Repeat("x", 8000)}
	h := testHost(t, s, p, &hostModel{decisions: []Decision{d}})
	h.Settings.MaxStateBytes = 4096
	r := createTestHostRun(t, h)
	r, e := h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "failed" || p.calls != 0 {
		t.Fatalf("budget: %+v %v calls=%d", r, e, p.calls)
	}
}
func TestHostModelRepairCheckpoint(t *testing.T) {
	s := testStore(t)
	p := &hostProvider{}
	m := &hostModel{decisions: []Decision{{Schema: "wrong", Kind: "final", AnswerMarkdown: "invalid"}, {Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "valid"}}}
	h := testHost(t, s, p, m)
	r := createTestHostRun(t, h)
	m.hook = func(c ContextPacket) {
		stored, e := s.GetRun(r.RunID, "alice")
		if e != nil {
			t.Error(e)
			return
		}
		runtime := stored.State["runtime"].(map[string]any)
		n := fmt.Sprint(runtime["rounds_used"])
		if n != fmt.Sprint(m.calls+1) {
			t.Errorf("round not checkpointed before request: %s", n)
		}
	}
	r, e := h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "completed" || m.calls != 2 {
		t.Fatalf("repair: %+v %v calls=%d", r, e, m.calls)
	}
	state, callErr6 := h.restore(r)
	if callErr6 != nil {
		t.Error(callErr6)
	}
	if state.RoundsUsed != 2 {
		t.Fatalf("charged rounds %d", state.RoundsUsed)
	}
}
func TestCanonicalJSONCompatibility(t *testing.T) {
	b, e := CanonicalJSON(map[string]any{"z": json.Number("1.0"), "a": "记录 <A>&", "nested": map[string]any{"b": 2, "a": 1}})
	want := `{"a":"记录 <A>&","nested":{"a":1,"b":2},"z":1.0}`
	if e != nil || string(b) != want {
		t.Fatalf("canonical %s %v", b, e)
	}
}

func TestPythonProviderFingerprintCompatibility(t *testing.T) {
	provider, err := LoadRestPack("testdata/rest-v2.json", map[string]string{"RECORDS_URL": "https://records.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(provider.Close)
	want, err := os.ReadFile("testdata/python-rest-fingerprint.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := fingerprint(provider); got != strings.TrimSpace(string(want)) {
		t.Fatalf("Python fingerprint mismatch: got %s want %s", got, want)
	}
}

func TestPythonOperationFingerprintCompatibility(t *testing.T) {
	provider, err := LoadRestPack("testdata/rest-operation.json", map[string]string{"RECORDS_URL": "https://records.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(provider.Close)
	want, err := os.ReadFile("testdata/python-operation-fingerprint.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := fingerprint(provider); got != strings.TrimSpace(string(want)) {
		t.Fatalf("Python operation fingerprint mismatch: got %s want %s", got, want)
	}
}
