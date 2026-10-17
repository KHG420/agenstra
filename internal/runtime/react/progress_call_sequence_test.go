package react

import (
	"fmt"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestProgressPreservesDistinctCompletedCallsInOrder(t *testing.T) {
	state := &agentcontract.RuntimeState{}
	for i, name := range []string{"records.read", "records.update", "records.read", "records.update", "records.read"} {
		state.Observations = append(state.Observations, agentcontract.Observation{
			Capability: name, CallRef: fmt.Sprintf("step-%d", i), Status: "succeeded", FactID: agentcontract.Strptr(agentcontract.NewID()),
		})
	}
	view := RunProgress(state, 8)
	if view.CompletedCount != 5 || len(view.Completed) != 5 || view.OmittedItems != 0 {
		t.Fatalf("distinct completed calls were collapsed by capability: %+v", view)
	}
	for i, item := range view.Completed {
		if item.CallRef != state.Observations[i].CallRef || item.FactID == nil || *item.FactID != *state.Observations[i].FactID {
			t.Fatalf("completed call order or evidence changed at %d: %+v", i, view)
		}
	}
}

func TestProgressKeepsLatestOutcomePerCallWithoutMergingOtherCalls(t *testing.T) {
	state := &agentcontract.RuntimeState{Observations: []agentcontract.Observation{
		{Capability: "records.read", CallRef: "first", Status: "succeeded", FactID: agentcontract.Strptr("old")},
		{Capability: "records.read", CallRef: "second", Status: "succeeded", FactID: agentcontract.Strptr("second-result")},
		{Capability: "records.read", CallRef: "first", Status: "succeeded", FactID: agentcontract.Strptr("current")},
	}}
	view := RunProgress(state, 8)
	if view.CompletedCount != 2 || len(view.Completed) != 2 || view.OmittedItems != 0 ||
		view.Completed[0].CallRef != "second" || *view.Completed[0].FactID != "second-result" ||
		view.Completed[1].CallRef != "first" || *view.Completed[1].FactID != "current" {
		t.Fatalf("latest call outcome suppressed a different completed call: %+v", view)
	}
}
