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

func TestEvaluateRunRetainsCitationsOfCompactlyApprovedAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*agentcontract.RuntimeState)
		want   bool
	}{
		{name: "exact approved answer", want: true},
		{name: "changed published answer", mutate: func(state *agentcontract.RuntimeState) { state.AnswerMarkdown = "different answer" }},
		{name: "intervening decision", mutate: func(state *agentcontract.RuntimeState) {
			state.Decisions = append(state.Decisions[:1], agentcontract.JSON{"kind": "search_capabilities", "query": "orders"}, state.Decisions[1])
		}},
		{name: "approval with replacement references", mutate: func(state *agentcontract.RuntimeState) {
			state.Decisions[1]["fact_ids"] = []string{state.Facts[0].FactID}
		}},
		{name: "newer contradictory evidence", mutate: func(state *agentcontract.RuntimeState) {
			state.Facts = append(state.Facts, agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "orders.get", Value: agentcontract.JSON{"approved": false}})
		}},
		{name: "uncited read", mutate: func(state *agentcontract.RuntimeState) { state.Decisions[0]["fact_ids"] = []string{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := agentcontract.NewID()
			fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "orders.get", Value: agentcontract.JSON{"approved": true}}
			state := agentcontract.RuntimeState{RunID: id, Status: "completed", AnswerMarkdown: "Approved and read back", Facts: []agentcontract.Fact{fact},
				Observations: []agentcontract.Observation{{Capability: fact.SourceCapability, Status: "succeeded"}},
				Decisions: []agentcontract.JSON{
					{"schema": "agenstra.decision.v1", "kind": "final", "answer_markdown": "Approved and read back", "fact_ids": []string{fact.FactID}},
					{"schema": "agenstra.decision.v1", "kind": "approve_completion"},
				}}
			if tc.mutate != nil {
				tc.mutate(&state)
			}
			run := agentcontract.StoredRun{RunID: id, PackID: "orders", Status: "completed", State: agentcontract.JSON{"runtime": state}}
			c := agentcontract.EvaluationCase{Name: "approved read", PackID: "orders", RunID: id, Facts: []agentcontract.FactRequirement{{Capability: fact.SourceCapability, Path: []any{"approved"}, Value: true}}}
			result, err := EvaluateRun(t.Context(), run, c)
			if err != nil || result.Passed != tc.want {
				t.Fatalf("approved draft evaluation: passed=%t want=%t result=%+v error=%v", result.Passed, tc.want, result, err)
			}
		})
	}
}
