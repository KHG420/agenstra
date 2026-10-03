package agenstra

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOperationContextPointsToRetainedPollEvidence(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, MaxContextCharacters: 80000}
	state, callErr := r.NewState("Read current activity", "")
	if callErr != nil {
		t.Error(callErr)
	}
	initial, completed := NewID(), NewID()
	ref := "read-1"
	state.Facts = []Fact{{FactID: completed, SourceCapability: "ui.command_status", ReferenceScope: "durable", Value: JSON{"data": JSON{"status": "succeeded", "result": JSON{"count": 156}}}}}
	state.Observations = []Observation{
		{CallRef: ref, Capability: "ui.read_activity", Status: "succeeded", FactID: strptr(initial), Arguments: JSON{}},
		{CallRef: "poll-" + deterministicInvocationID(state.RunID, ref) + "-1", Capability: "ui.command_status", Status: "succeeded", FactID: strptr(completed), Arguments: JSON{"command_id": "command-1"}},
	}
	state.ModelObservations = append([]Observation{}, state.Observations...)
	before, callErr2 := CanonicalJSON(state)
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
	after, callErr3 := CanonicalJSON(state)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("model projection changed the durable audit trail")
	}
}

func TestOperationContextDoesNotBorrowAnotherInvocationsEvidence(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}}
	state, callErr4 := r.NewState("Read", "")
	if callErr4 != nil {
		t.Error(callErr4)
	}
	initial, other, retained := NewID(), NewID(), NewID()
	state.Facts = []Fact{{FactID: retained}, {FactID: other}}
	state.Observations = []Observation{
		{CallRef: "read-1", Capability: "records.read", Status: "succeeded", FactID: strptr(initial)},
		{CallRef: "read-2", Capability: "records.read", Status: "succeeded", FactID: strptr(other)},
		{CallRef: "poll-" + deterministicInvocationID(state.RunID, "read-2") + "-1", Capability: "records.status", Status: "succeeded", FactID: strptr(retained)},
	}
	view := currentEvidenceObservations(state, state.Observations)
	if *view[0].FactID != initial || *view[1].FactID != other {
		t.Fatal("unrelated or still-available evidence was replaced", view)
	}
	// A retained poll from the same invocation is eligible even with several
	// prior polls, but a discarded poll or an error is not replacement evidence.
	state.Observations = append(state.Observations,
		Observation{CallRef: "poll-" + deterministicInvocationID(state.RunID, "read-1") + "-1", FactID: strptr(NewID())},
		Observation{CallRef: "poll-" + deterministicInvocationID(state.RunID, "read-1") + "-2", FactID: strptr(retained), ErrorCode: strptr("provider_outcome_unknown")})
	view = currentEvidenceObservations(state, state.Observations)
	if *view[0].FactID != initial {
		t.Fatal("unavailable or failed poll was used as replacement evidence", view)
	}
	state.Observations = append(state.Observations, Observation{CallRef: "poll-" + deterministicInvocationID(state.RunID, "read-1") + "-3", FactID: strptr(retained)})
	view = currentEvidenceObservations(state, state.Observations)
	if *view[0].FactID != retained {
		t.Fatal("retained poll from the same invocation was not projected", view)
	}
}

func TestOperationContextPreservesFailedAndUnknownOutcomes(t *testing.T) {
	for _, status := range []string{"failed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}}
			state, callErr5 := r.NewState("Run operation", "")
			if callErr5 != nil {
				t.Error(callErr5)
			}
			initial, retained := NewID(), NewID()
			ref := "job-1"
			state.Facts = []Fact{{FactID: retained, Value: JSON{"data": JSON{"status": status}}}}
			state.Observations = []Observation{
				{CallRef: ref, Capability: "job.start", Status: "succeeded", FactID: strptr(initial)},
				{CallRef: "poll-" + deterministicInvocationID(state.RunID, ref) + "-1", Capability: "job.status", Status: "succeeded", FactID: strptr(retained)},
			}
			state.ModelObservations = append([]Observation{}, state.Observations...)
			state.Pending = []Invocation{{Call: ToolCall{CallRef: ref, Capability: "job.start"}, Status: status}}
			if status == "failed" {
				Reject(state, ref, "job.start", "operation_failed", nil, "")
			}
			before, callErr6 := CanonicalJSON(state)
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
			after, callErr7 := CanonicalJSON(state)
			if callErr7 != nil {
				t.Error(callErr7)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("model projection changed the failed/unknown audit state")
			}
		})
	}
}

func TestBrowserCatalogKeepsExecutionHintsWithoutRepeatingHostPollConfiguration(t *testing.T) {
	binding := &OperationBinding{IDPath: []any{"command_id"}, StatusPath: []any{"status"}, PollCapability: "ui.command_status", PollArgument: []string{"command_id"}, PendingStates: []string{"queued", "dispatched", "running"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed", "expired", "cancelled"}, ReconciliationStates: []string{"unknown"}, ReconcileOnTimeout: true, IntervalSeconds: 1, TimeoutSeconds: 60}
	capability := CapabilityDescription{Name: "ui.read_activity", InputSchema: JSON{"type": "object"}, Effect: "read", Operation: binding}
	before, callErr8 := json.Marshal(capability)
	if callErr8 != nil {
		t.Error(callErr8)
	}
	view := capability.ModelView()
	operation := view["operation"].(map[string]any)
	if len(operation) != 1 || operation["poll_capability"] != "ui.command_status" || view["requires_browser_context"] != true {
		t.Fatal("catalog must describe the context prerequisite and result source concisely", view)
	}
	after, callErr9 := json.Marshal(capability)
	if callErr9 != nil {
		t.Error(callErr9)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("model catalog changed the provider contract")
	}
	capability.Name, capability.Operation.PollCapability = "job.start", "job.status"
	if _, ok := capability.ModelView()["operation"].(map[string]any)["poll_argument"]; !ok {
		t.Fatal("non-browser operation details were omitted")
	}
}
