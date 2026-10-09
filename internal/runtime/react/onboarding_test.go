package react

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestEvaluateRunRequiresLatestCitedBusinessEvidence(t *testing.T) {
	id := agentcontract.NewID()
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "orders.approve", Value: agentcontract.JSON{"approved": true}}
	state := agentcontract.RuntimeState{RunID: id, Status: "completed", Facts: []agentcontract.Fact{fact}, Observations: []agentcontract.Observation{{Capability: fact.SourceCapability, Status: "succeeded"}}, Decisions: []agentcontract.JSON{{"kind": "final", "fact_ids": []string{fact.FactID}}}}
	run := agentcontract.StoredRun{RunID: id, PackID: "orders", Status: "completed", State: agentcontract.JSON{"runtime": state}}
	c := agentcontract.EvaluationCase{Name: "approve", PackID: "orders", Instruction: "Approve", RequiredCapabilities: []string{fact.SourceCapability}, ForbiddenCapabilities: []string{"orders.delete"}, Facts: []agentcontract.FactRequirement{{Capability: fact.SourceCapability, Path: []any{"approved"}, Value: true}}}
	result, err := EvaluateRun(t.Context(), run, c)
	if err != nil || !result.Passed {
		t.Fatal(result, err)
	}
	state.Facts = append(state.Facts, agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: fact.SourceCapability, Value: agentcontract.JSON{"approved": false}})
	run.State["runtime"] = state
	result, err = EvaluateRun(t.Context(), run, c)
	if err != nil || result.Passed {
		t.Fatal("older evidence passed", result, err)
	}
	state.Facts = []agentcontract.Fact{fact}
	state.Pending = []agentcontract.Invocation{{Call: agentcontract.ToolCall{Capability: "orders.delete"}}}
	run.State["runtime"] = state
	result, err = EvaluateRun(t.Context(), run, c)
	if err != nil || result.Passed {
		t.Fatal("forbidden operation passed", result, err)
	}
}
