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

func TestReconciledBrowserContextKeepsOneCurrentActionResult(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}}
	state, err := r.NewState("Report the verified original result", "")
	if err != nil {
		t.Fatal(err)
	}
	ref := "write-1"
	id := DeterministicInvocationID(state.RunID, ref)
	unknown, reconciled := agentcontract.NewID(), agentcontract.NewID()
	state.Facts = []agentcontract.Fact{
		{FactID: unknown, SourceCapability: "ui.command_status", Value: agentcontract.JSON{"data": agentcontract.JSON{"command_id": id, "status": "unknown"}}},
		{FactID: reconciled, SourceCapability: "ui.command_status", Quality: "verified_reconciliation", Value: agentcontract.JSON{"data": agentcontract.JSON{"command_id": id, "status": "succeeded", "result": agentcontract.JSON{"count": 1}}}},
	}
	state.Observations = []agentcontract.Observation{
		{CallRef: ref, Capability: "ui.update_record", Status: "succeeded", FactID: agentcontract.Strptr(agentcontract.NewID())},
		{CallRef: "poll-" + id + "-1", Capability: "ui.command_status", Status: "succeeded", FactID: agentcontract.Strptr(unknown)},
		{CallRef: ref, Capability: "ui.update_record", Status: "succeeded", FactID: agentcontract.Strptr(reconciled)},
	}
	state.ModelObservations = append([]agentcontract.Observation{}, state.Observations...)
	before, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	packet := r.Context(state)
	if len(packet.Observations) != 1 || packet.Observations[0].FactID == nil || *packet.Observations[0].FactID != reconciled {
		t.Errorf("reconciliation left duplicate/stale action evidence: %+v", packet.Observations)
	}
	progress := RunProgress(state, 8)
	if progress.CompletedCount != 1 || len(progress.Completed) != 1 || *progress.Completed[0].FactID != reconciled {
		t.Errorf("reconciliation counted an obsolete poll as another completion: %+v", progress)
	}
	after, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("current evidence projection changed immutable history")
	}
}

func TestReconciledContextDoesNotSuppressUnrelatedPolls(t *testing.T) {
	for _, mismatch := range []string{"command", "source", "quality"} {
		t.Run(mismatch, func(t *testing.T) {
			state := &agentcontract.RuntimeState{RunID: agentcontract.NewID()}
			ref := "write-1"
			id := DeterministicInvocationID(state.RunID, ref)
			unknown, reconciled := agentcontract.NewID(), agentcontract.NewID()
			fact := agentcontract.Fact{FactID: reconciled, SourceCapability: "ui.command_status", Quality: "verified_reconciliation", Value: agentcontract.JSON{"data": agentcontract.JSON{"command_id": id}}}
			switch mismatch {
			case "command":
				fact.Value = agentcontract.JSON{"data": agentcontract.JSON{"command_id": "another-command"}}
			case "source":
				fact.SourceCapability = "records.read"
			case "quality":
				fact.Quality = "observed"
			}
			state.Facts = []agentcontract.Fact{{FactID: unknown}, fact}
			state.Observations = []agentcontract.Observation{
				{CallRef: "poll-" + id + "-1", Capability: "ui.command_status", Status: "succeeded", FactID: agentcontract.Strptr(unknown)},
				{CallRef: ref, Capability: "ui.update_record", Status: "succeeded", FactID: agentcontract.Strptr(reconciled)},
				{CallRef: "another-write", Capability: "ui.update_record", Status: "succeeded", FactID: agentcontract.Strptr(reconciled)},
			}
			view := currentEvidenceObservations(state, state.Observations)
			if len(view) != 3 || *view[1].FactID != reconciled || *view[2].FactID != reconciled {
				t.Fatal("unrelated poll or a distinct invocation was removed", view)
			}
		})
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
