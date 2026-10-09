package host

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestContextPolicyJournalReplayAndSafeApplication(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	id := agentcontract.NewID()
	policy := agentcontract.ContextPolicy{TriggerRatio: .8, TargetRatio: .6}
	if _, err := h.SetContextPolicy(t.Context(), run.RunID, "alice", id, run.Revision, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetContextPolicy(t.Context(), run.RunID, "alice", id, 999, policy); err != nil {
		t.Fatal("lost-response replay", err)
	}
	if _, err := h.SetContextPolicy(t.Context(), run.RunID, "alice", id, run.Revision, agentcontract.ContextPolicy{TriggerRatio: .9, TargetRatio: .5}); agentcontract.ErrorCode(err) != "context_policy_request_conflict" {
		t.Fatal(err)
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, callErr2 := h.Restore(run)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if state.ContextPolicy == nil || *state.ContextPolicy != policy || state.ContextPolicyCursor == 0 {
		t.Fatalf("%+v", state)
	}
	if state.ContextTelemetry.Policy != policy {
		t.Fatal(state.ContextTelemetry)
	}
	if _, err = h.SetContextPolicy(t.Context(), run.RunID, "bob", agentcontract.NewID(), run.Revision, policy); err == nil {
		t.Fatal("owner isolation")
	}
}

func TestContextPolicyAcceptedDuringModelCannotBeOvertakenByCompletion(t *testing.T) {
	m := &hostModel{}
	h := testHost(t, testStore(t), &hostProvider{}, m)
	run := createTestHostRun(t, h)
	m.hook = func(agentcontract.ContextPacket) {
		current, err := h.Store.GetRun(run.RunID, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.SetContextPolicy(t.Context(), run.RunID, "alice", agentcontract.NewID(), current.Revision, agentcontract.ContextPolicy{TriggerRatio: .8, TargetRatio: .6}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, callErr3 := h.Restore(run)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if run.Status != "completed" || state.ContextPolicyCursor == 0 {
		t.Fatalf("%+v", state)
	}
}
