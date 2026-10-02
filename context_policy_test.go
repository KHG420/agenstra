package agenstra

import (
	"strings"
	"testing"
)

func TestContextPolicySoftTargetAndRequiredInformation(t *testing.T) {
	r, state, _ := contextBudgetRuntime(t, 30000)
	r.ContextPolicy = ContextPolicy{.4, .3}
	for i := 0; i < 10; i++ {
		state.Facts = append(state.Facts, contextBudgetFact(JSON{"body": strings.Repeat("x", 1000)}))
	}
	r.Model = &coreTestModel{decisions: []Decision{{Kind: "request_input", Field: "x", Prompt: "x"}}}
	_ = r.Step(t.Context(), state, nil)
	c := state.ContextTelemetry
	if c.ProjectionReason != "soft_threshold" || !c.TargetMet || float64(c.InputCharacters) > float64(c.CharacterLimit)*.3 || len(state.Facts) != 10 {
		t.Fatalf("%+v", c)
	}
}

func TestContextPolicyJournalReplayAndSafeApplication(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	id := NewID()
	policy := ContextPolicy{.8, .6}
	if _, err := h.SetContextPolicy(t.Context(), run.RunID, "alice", id, run.Revision, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetContextPolicy(t.Context(), run.RunID, "alice", id, 999, policy); err != nil {
		t.Fatal("lost-response replay", err)
	}
	if _, err := h.SetContextPolicy(t.Context(), run.RunID, "alice", id, run.Revision, ContextPolicy{.9, .5}); ErrorCode(err) != "context_policy_request_conflict" {
		t.Fatal(err)
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, _ := h.restore(run)
	if state.ContextPolicy == nil || *state.ContextPolicy != policy || state.ContextPolicyCursor == 0 {
		t.Fatalf("%+v", state)
	}
	if state.ContextTelemetry.Policy != policy {
		t.Fatal(state.ContextTelemetry)
	}
	if _, err = h.SetContextPolicy(t.Context(), run.RunID, "bob", NewID(), run.Revision, policy); err == nil {
		t.Fatal("owner isolation")
	}
}

func TestContextPolicyAcceptedDuringModelCannotBeOvertakenByCompletion(t *testing.T) {
	m := &hostModel{}
	h := testHost(t, testStore(t), &hostProvider{}, m)
	run := createTestHostRun(t, h)
	m.hook = func(ContextPacket) {
		current, err := h.Store.GetRun(run.RunID, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.SetContextPolicy(t.Context(), run.RunID, "alice", NewID(), current.Revision, ContextPolicy{.8, .6}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, _ := h.restore(run)
	if run.Status != "completed" || state.ContextPolicyCursor == 0 {
		t.Fatalf("%+v", state)
	}
}
