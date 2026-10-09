package react

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestContextPolicySoftTargetAndRequiredInformation(t *testing.T) {
	r, state, _ := contextBudgetRuntime(t, 30000)
	r.ContextPolicy = agentcontract.ContextPolicy{TriggerRatio: .4, TargetRatio: .3}
	for i := 0; i < 10; i++ {
		state.Facts = append(state.Facts, contextBudgetFact(agentcontract.JSON{"body": strings.Repeat("x", 1000)}))
	}
	r.Model = &coreTestModel{decisions: []agentcontract.Decision{{Kind: "request_input", Field: "x", Prompt: "x"}}}
	if callErr := r.Step(t.Context(), state, nil); callErr != nil {
		t.Error(callErr)
	}
	c := state.ContextTelemetry
	if c.ProjectionReason != "soft_threshold" || !c.TargetMet || float64(c.InputCharacters) > float64(c.CharacterLimit)*.3 || len(state.Facts) != 10 {
		t.Fatalf("%+v", c)
	}
}
