package react

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestDecisionTimeoutKeepsUnknownBoundedModelBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		metrics agentcontract.ModelCallMetrics
		budget  int64
		reserve bool
	}{
		{name: "unknown", metrics: agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}, budget: 10000, reserve: true},
		{name: "reported", metrics: agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 50, OutputTokens: 20}, budget: 70},
		{name: "not sent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
				return agentcontract.Decision{ModelCall: &tc.metrics}, context.DeadlineExceeded
			})
			runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelTokens: 10000}
			result, err := runtime.Run(t.Context(), "reply")
			if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != "model_timeout" {
				t.Fatalf("status=%s code=%v error=%v", result.Status, result.ErrorCode, err)
			}
			if len(result.ModelCalls) != 1 || result.ModelCalls[0].Reservation != tc.reserve || result.ModelUsage.BudgetTokens != tc.budget || result.ModelUsage.InputTokens != tc.metrics.InputTokens || result.ModelUsage.OutputTokens != tc.metrics.OutputTokens {
				t.Fatalf("decision timeout settled unknown output as zero: calls=%+v usage=%+v", result.ModelCalls, result.ModelUsage)
			}
		})
	}
}
