package agenstra

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestProgressRetainsEarlierOutcomesAndPendingIsUnfinished(t *testing.T) {
	state := &RuntimeState{}
	for i := 0; i < 20; i++ {
		capability := "recent.read"
		if i == 0 {
			capability = "earlier.compute"
		}
		obs := Observation{CallRef: fmt.Sprintf("read-%d", i), Capability: capability, Status: "succeeded", FactID: strptr(NewID())}
		state.Observations = append(state.Observations, obs)
		state.ModelObservations = append(state.ModelObservations, obs)
	}
	state.Pending = []Invocation{{Call: ToolCall{CallRef: "job", Capability: "job.submit"}, Status: "waiting"}}
	state.Observations = append(state.Observations, Observation{CallRef: "job", Capability: "job.submit", Status: "succeeded"})
	Reject(state, "blocked", "missing.read", "capability_not_granted", nil, "")
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}}
	packet := r.Context(state)
	if packet.Progress.CompletedCount != 20 || len(packet.Progress.Completed) != 2 || packet.Progress.Completed[0].Capability != "earlier.compute" || len(packet.Progress.Pending) != 1 || packet.Progress.BlockedCount != 1 {
		t.Fatalf("%+v", packet.Progress)
	}
	if packet.Observations[0].Capability == "earlier.compute" {
		t.Fatal("fixture did not exceed observation window")
	}
}

func TestStagnationDetectsInspectionCyclesAndSurvivesRestore(t *testing.T) {
	calls, warned := 0, false
	m := decisionModelFunc(func(_ context.Context, packet ContextPacket, _ string) (Decision, error) {
		calls++
		warned = warned || (packet.Progress != nil && packet.Progress.StagnationWarning)
		name := "a.read"
		if calls%2 == 0 {
			name = "b.read"
		}
		return Decision{Kind: "inspect_capability", Name: name}, nil
	})
	p := &coreTestProvider{caps: map[string]CapabilityDescription{"a.read": {Name: "a.read"}, "b.read": {Name: "b.read"}}}
	r := &AgentRuntime{Provider: p, Model: m, Grants: map[string]bool{"a.read": true, "b.read": true}, MaxStagnantRounds: 3}
	state, _ := r.NewState("inspect", "")
	for i := 0; i < 5; i++ {
		if err := r.Step(t.Context(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := CanonicalJSON(state)
	var restored RuntimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := r.Step(t.Context(), &restored, nil); err != nil || restored.Status != "failed" || *restored.ErrorCode != "agent_stagnated" || calls != 5 || !warned {
		t.Fatalf("state=%+v calls=%d warning=%v err=%v", restored, calls, warned, err)
	}
}

func TestProgressCountsNewEvidenceAndInputButNotNewFactIDs(t *testing.T) {
	state := &RuntimeState{}
	if updateProgress(state, 3, nil) {
		t.Fatal("initial stagnation")
	}
	f := Fact{FactID: NewID(), SourceCapability: "records.read", Value: JSON{"count": 1}}
	state.Facts = append(state.Facts, f)
	updateProgress(state, 3, nil)
	f.FactID = NewID()
	state.Facts = append(state.Facts, f)
	updateProgress(state, 3, nil)
	if state.Progress.NoProgressRounds != 1 {
		t.Fatal("duplicate content counted as progress")
	}
	f.Value = JSON{"count": 2}
	state.Facts = append(state.Facts, f)
	updateProgress(state, 3, nil)
	if state.Progress.NoProgressRounds != 0 {
		t.Fatal("new data did not reset stagnation")
	}
	updateProgress(state, 3, nil)
	state.Followups = append(state.Followups, "Use the second result")
	updateProgress(state, 3, nil)
	if state.Progress.NoProgressRounds != 0 {
		t.Fatal("new user input did not reset stagnation")
	}
}

func TestProgressProjectionIsBoundedAndDoesNotMutateState(t *testing.T) {
	state := &RuntimeState{}
	for i := 0; i < 40; i++ {
		state.Observations = append(state.Observations, Observation{CallRef: fmt.Sprintf("ref-%d", i), Capability: fmt.Sprintf("tool.%02d", i), Status: "succeeded"})
	}
	before, _ := CanonicalJSON(state)
	view := runProgress(state, 8)
	if len(view.Completed) != 8 || view.CompletedCount != 40 || view.OmittedItems != 32 {
		t.Fatalf("%+v", view)
	}
	after, _ := CanonicalJSON(state)
	if string(before) != string(after) {
		t.Fatal("projection mutated audit state")
	}
}
