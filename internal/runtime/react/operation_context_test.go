package react

import (
	"reflect"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestOperationContextPointsToRetainedPollEvidence(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, MaxContextCharacters: 80000}
	state, callErr := r.NewState("Read current activity", "")
	if callErr != nil {
		t.Error(callErr)
	}
	initial, completed := agentcontract.NewID(), agentcontract.NewID()
	ref := "read-1"
	state.Facts = []agentcontract.Fact{{FactID: completed, SourceCapability: "ui.command_status", ReferenceScope: "durable", Value: agentcontract.JSON{"data": agentcontract.JSON{"status": "succeeded", "result": agentcontract.JSON{"count": 156}}}}}
	state.Observations = []agentcontract.Observation{
		{CallRef: ref, Capability: "ui.read_activity", Status: "succeeded", FactID: agentcontract.Strptr(initial), Arguments: agentcontract.JSON{}},
		{CallRef: "poll-" + DeterministicInvocationID(state.RunID, ref) + "-1", Capability: "ui.command_status", Status: "succeeded", FactID: agentcontract.Strptr(completed), Arguments: agentcontract.JSON{"command_id": "command-1"}},
	}
	state.ModelObservations = append([]agentcontract.Observation{}, state.Observations...)
	before, callErr2 := agentcontract.CanonicalJSON(state)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	packet := r.Context(state)
	if packet.Observations[0].FactID == nil || *packet.Observations[0].FactID != completed {
		t.Fatal("model was directed to a discarded queued receipt", packet.Observations[0])
	}
	found := false
	for _, item := range packet.Progress.Completed {
		if item.Capability == "ui.read_activity" {
			found = true
			if item.FactID == nil || *item.FactID != completed {
				t.Fatal("progress was directed to a discarded queued receipt", item)
			}
		}
	}
	if !found {
		t.Fatal("progress omitted the completed host action")
	}
	after, callErr3 := agentcontract.CanonicalJSON(state)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("model projection changed the durable audit trail")
	}
}

func TestOperationContextDoesNotBorrowAnotherInvocationsEvidence(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}}
	state, callErr4 := r.NewState("Read", "")
	if callErr4 != nil {
		t.Error(callErr4)
	}
	initial, other, retained := agentcontract.NewID(), agentcontract.NewID(), agentcontract.NewID()
	state.Facts = []agentcontract.Fact{{FactID: retained}, {FactID: other}}
	state.Observations = []agentcontract.Observation{
		{CallRef: "read-1", Capability: "records.read", Status: "succeeded", FactID: agentcontract.Strptr(initial)},
		{CallRef: "read-2", Capability: "records.read", Status: "succeeded", FactID: agentcontract.Strptr(other)},
		{CallRef: "poll-" + DeterministicInvocationID(state.RunID, "read-2") + "-1", Capability: "records.status", Status: "succeeded", FactID: agentcontract.Strptr(retained)},
	}
	view := currentEvidenceObservations(state, state.Observations)
	if *view[0].FactID != initial || *view[1].FactID != other {
		t.Fatal("unrelated or still-available evidence was replaced", view)
	}

	// A retained poll from the same invocation is eligible even with several
	// prior polls, but a discarded poll or an error is not replacement evidence.
	state.Observations = append(state.Observations,
		agentcontract.Observation{CallRef: "poll-" + DeterministicInvocationID(state.RunID, "read-1") + "-1", FactID: agentcontract.Strptr(agentcontract.NewID())},
		agentcontract.Observation{CallRef: "poll-" + DeterministicInvocationID(state.RunID, "read-1") + "-2", FactID: agentcontract.Strptr(retained), ErrorCode: agentcontract.Strptr("provider_outcome_unknown")})
	view = currentEvidenceObservations(state, state.Observations)
	if *view[0].FactID != initial {
		t.Fatal("unavailable or failed poll was used as replacement evidence", view)
	}
	state.Observations = append(state.Observations, agentcontract.Observation{CallRef: "poll-" + DeterministicInvocationID(state.RunID, "read-1") + "-3", FactID: agentcontract.Strptr(retained)})
	view = currentEvidenceObservations(state, state.Observations)
	if *view[0].FactID != retained {
		t.Fatal("retained poll from the same invocation was not projected", view)
	}
}

func TestOperationContextPreservesFailedAndUnknownOutcomes(t *testing.T) {
	for _, status := range []string{"failed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}}
			state, callErr5 := r.NewState("Run operation", "")
			if callErr5 != nil {
				t.Error(callErr5)
			}
			initial, retained := agentcontract.NewID(), agentcontract.NewID()
			ref := "job-1"
			state.Facts = []agentcontract.Fact{{FactID: retained, Value: agentcontract.JSON{"data": agentcontract.JSON{"status": status}}}}
			state.Observations = []agentcontract.Observation{
				{CallRef: ref, Capability: "job.start", Status: "succeeded", FactID: agentcontract.Strptr(initial)},
				{CallRef: "poll-" + DeterministicInvocationID(state.RunID, ref) + "-1", Capability: "job.status", Status: "succeeded", FactID: agentcontract.Strptr(retained)},
			}
			state.ModelObservations = append([]agentcontract.Observation{}, state.Observations...)
			state.Pending = []agentcontract.Invocation{{Call: agentcontract.ToolCall{CallRef: ref, Capability: "job.start"}, Status: status}}
			if status == "failed" {
				Reject(state, ref, "job.start", "operation_failed", nil, "")
			}
			before, callErr6 := agentcontract.CanonicalJSON(state)
			if callErr6 != nil {
				t.Error(callErr6)
			}
			packet := r.Context(state)
			for _, item := range packet.Progress.Completed {
				if item.Capability == "job.start" {
					t.Fatal("unsuccessful operation was presented as completed", item)
				}
			}
			if status == "failed" && (len(packet.Progress.Blocked) != 1 || *packet.Progress.Blocked[0].ErrorCode != "operation_failed") {
				t.Fatal("operation failure was erased", packet.Progress)
			}
			if status == "unknown" && (len(packet.Progress.Pending) != 1 || packet.Progress.Pending[0].Status != "unknown") {
				t.Fatal("uncertain operation was no longer pending", packet.Progress)
			}
			after, callErr7 := agentcontract.CanonicalJSON(state)
			if callErr7 != nil {
				t.Error(callErr7)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("model projection changed the failed/unknown audit state")
			}
		})
	}
}
