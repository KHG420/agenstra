package react

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestUnknownModelWindowIsNotReportedAsZero(t *testing.T) {
	r, state, _ := contextBudgetRuntime(t, 9000)
	r.Model = &coreTestModel{decisions: []agentcontract.Decision{{Kind: "request_input", Field: "x", Prompt: "x"}}}
	if callErr6 := r.Step(t.Context(), state, nil); callErr6 != nil {
		t.Error(callErr6)
	}
	c := state.ContextTelemetry
	if c.ModelContextWindowTokens != nil || c.EffectiveInputTokenLimit != nil || c.TokensRemaining != nil || c.InputTokens == nil {
		t.Fatalf("%+v", c)
	}
}
