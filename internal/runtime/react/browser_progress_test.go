package react

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func appendBrowserProgressResult(state *agentcontract.RuntimeState, action, ref string, result agentcontract.JSON) string {
	id := DeterministicInvocationID(state.RunID, ref)
	factID := agentcontract.NewID()
	state.Facts = append(state.Facts, agentcontract.Fact{FactID: factID, SourceCapability: "ui.command_status", ReferenceScope: "durable", Value: agentcontract.JSON{"data": agentcontract.JSON{"command_id": id, "status": "succeeded", "result": result}}})
	observations := []agentcontract.Observation{
		{CallRef: ref, Capability: action, Status: "succeeded", FactID: agentcontract.Strptr(agentcontract.NewID())},
		{CallRef: "poll-" + id + "-1", Capability: "ui.command_status", Status: "succeeded", FactID: agentcontract.Strptr(factID)},
	}
	state.Observations = append(state.Observations, observations...)
	state.ModelObservations = append(state.ModelObservations, observations...)
	return factID
}

func TestBrowserProgressCountsActionsWithoutDuplicatePolls(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}}
	state, callErr := r.NewState("Read six independent results", "")
	if callErr != nil {
		t.Error(callErr)
	}
	for i := 0; i < 6; i++ {
		appendBrowserProgressResult(state, fmt.Sprintf("ui.read_%d", i), fmt.Sprintf("read-%d", i), agentcontract.JSON{"count": i})
	}
	before, callErr2 := agentcontract.CanonicalJSON(state)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	packet := r.Context(state)
	if len(packet.Observations) != 6 || packet.Progress.CompletedCount != 6 || len(packet.Progress.Completed) != 6 {
		t.Fatalf("polls duplicated action evidence: observations=%d progress=%+v", len(packet.Observations), packet.Progress)
	}
	for _, observation := range packet.Observations {
		if observation.Capability == "ui.command_status" || observation.FactID == nil {
			t.Fatalf("expected original action with current result: %+v", observation)
		}
	}
	after, callErr3 := agentcontract.CanonicalJSON(state)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if string(before) != string(after) {
		t.Fatal("projection changed the audit trail")
	}
}

func TestBrowserReadReceiptsDoNotDisguiseStagnation(t *testing.T) {
	for _, inspect := range []bool{false, true} {
		t.Run(fmt.Sprintf("inspect=%t", inspect), func(t *testing.T) {
			cap := agentcontract.CapabilityDescription{Name: "ui.read_activity", Effect: "read", Operation: &agentcontract.OperationBinding{PollCapability: "ui.command_status"}}
			calls, warned := 0, false
			var latest string
			r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, Grants: map[string]bool{cap.Name: true}, MaxStagnantRounds: 3}
			r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				calls++
				warned = warned || packet.Progress.StagnationWarning
				if inspect {
					return agentcontract.Decision{Kind: "inspect_fact", FactID: latest, Path: []any{"data", "result"}}, nil
				}
				return agentcontract.Decision{Kind: "inspect_capability", Name: cap.Name}, nil
			})
			state, callErr4 := r.NewState("Read current activity", "")
			if callErr4 != nil {
				t.Error(callErr4)
			}
			for i := 0; i < 8 && state.Status != "failed"; i++ {
				latest = appendBrowserProgressResult(state, cap.Name, fmt.Sprintf("read-%d", i), agentcontract.JSON{"count": 156})
				if err := r.Step(t.Context(), state, nil); err != nil {
					t.Fatal(err)
				}
			}
			if state.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "agent_stagnated" || calls != 4 || !warned {
				t.Fatalf("new command IDs reset stagnation: status=%s error=%v calls=%d warning=%v", state.Status, state.ErrorCode, calls, warned)
			}
		})
	}
}

func TestBrowserProgressRecognizesBusinessChangesAndPreservesWrites(t *testing.T) {
	read := agentcontract.CapabilityDescription{Name: "ui.read_activity", Effect: "read", Operation: &agentcontract.OperationBinding{PollCapability: "ui.command_status"}}
	write := agentcontract.CapabilityDescription{Name: "ui.start_round", Effect: "write", Operation: read.Operation}
	caps := map[string]agentcontract.CapabilityDescription{read.Name: read, write.Name: write}
	state := &agentcontract.RuntimeState{RunID: agentcontract.NewID()}
	appendBrowserProgressResult(state, read.Name, "read-1", agentcontract.JSON{"count": 156})
	updateProgress(state, 3, caps)
	appendBrowserProgressResult(state, read.Name, "read-2", agentcontract.JSON{"count": 156})
	updateProgress(state, 3, caps)
	if state.Progress.NoProgressRounds != 1 {
		t.Fatal("identical read counted as progress")
	}
	before, callErr5 := agentcontract.CanonicalJSON(state)
	if callErr5 != nil {
		t.Error(callErr5)
	}
	var restored agentcontract.RuntimeState
	decoder := json.NewDecoder(bytes.NewReader(before))
	decoder.UseNumber()
	if err := decoder.Decode(&restored); err != nil {
		t.Fatal(err)
	}
	appendBrowserProgressResult(&restored, read.Name, "read-3", agentcontract.JSON{"count": 156})
	updateProgress(&restored, 3, caps)
	if restored.Progress.NoProgressRounds != 2 {
		t.Fatal("restoring a run reset its repeated-read detection")
	}
	appendBrowserProgressResult(&restored, read.Name, "read-4", agentcontract.JSON{"count": 153})
	updateProgress(&restored, 3, caps)
	if restored.Progress.NoProgressRounds != 0 {
		t.Fatal("changed business data did not count as progress")
	}
	for i := 0; i < 2; i++ {
		appendBrowserProgressResult(&restored, write.Name, fmt.Sprintf("write-%d", i), agentcontract.JSON{"count": 153})
		updateProgress(&restored, 3, caps)
		if restored.Progress.NoProgressRounds != 0 {
			t.Fatal("a distinct completed write was treated as a duplicate read")
		}
	}
}

func TestProgressKeepsStandalonePollsAndOrdinaryProviderIDs(t *testing.T) {
	state := &agentcontract.RuntimeState{RunID: agentcontract.NewID()}
	for i := 0; i < 2; i++ {
		fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "records.read", Value: agentcontract.JSON{"data": agentcontract.JSON{"command_id": fmt.Sprintf("record-%d", i), "status": "succeeded", "result": agentcontract.JSON{"count": 156}}}}
		state.Facts = append(state.Facts, fact)
		updateProgress(state, 3, nil)
		if state.Progress.NoProgressRounds != 0 {
			t.Fatal("a provider's business ID was discarded")
		}
	}
	ref := "poll-" + DeterministicInvocationID(state.RunID, "absent") + "-1"
	observation := agentcontract.Observation{CallRef: ref, Capability: "ui.command_status", Status: "succeeded", FactID: agentcontract.Strptr(state.Facts[0].FactID)}
	state.Observations = append(state.Observations, observation)
	view := currentEvidenceObservations(state, state.Observations)
	if len(view) != 1 || view[0].CallRef != ref {
		t.Fatal("poll evidence without its original action was lost", view)
	}
}
