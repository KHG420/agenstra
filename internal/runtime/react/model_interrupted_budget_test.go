package react

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestInterruptedDecisionKeepsBoundedBudget(t *testing.T) {
	metrics := agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}
	model := decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
		return agentcontract.Decision{ModelCall: &metrics}, agentcontract.ModelDecisionError{Kind: "model_outcome_unknown"}
	})
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelTokens: 10000}
	result, err := runtime.Run(t.Context(), "reply")
	if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != "model_outcome_unknown" {
		t.Fatalf("status=%s code=%v error=%v", result.Status, result.ErrorCode, err)
	}
	if len(result.ModelCalls) != 1 || !result.ModelCalls[0].Reservation || result.ModelCalls[0].UsageAvailable || result.ModelUsage.BudgetTokens != 10000 || result.ModelUsage.InputTokens != 0 || result.ModelUsage.OutputTokens != 0 {
		t.Fatalf("interrupted decision released dispatched allowance: calls=%+v usage=%+v", result.ModelCalls, result.ModelUsage)
	}
}
