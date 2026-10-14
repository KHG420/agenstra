package react

import (
	"context"
	"errors"
	"fmt"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCancelledDecisionKeepsOnlyUnknownDispatchedBoundedBudget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		limit, budget int64
		metrics       agentcontract.ModelCallMetrics
		reserve       bool
		failure       error
		code          string
	}{
		{name: "dispatched unknown", limit: 10000, budget: 10000, metrics: agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}, reserve: true},
		{name: "valid usage", limit: 10000, budget: 70, metrics: agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 50, OutputTokens: 20}},
		{name: "unsent", limit: 10000},
		{name: "unbounded", budget: 500, metrics: agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}},
		{name: "deliberate code wins", limit: 10000, budget: 500, metrics: agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}, failure: errors.Join(agentcontract.ModelDecisionError{Kind: "model_refused"}, context.Canceled), code: "model_refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
				failure := tc.failure
				if failure == nil {
					failure = fmt.Errorf("model-local cancellation: %w", context.Canceled)
				}
				return agentcontract.Decision{ModelCall: &tc.metrics}, failure
			})
			runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelTokens: tc.limit}
			result, err := runtime.Run(t.Context(), "reply")
			code := tc.code
			if code == "" {
				code = "model_unavailable"
			}
			if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != code {
				t.Fatalf("error classification changed: %+v %v", result, err)
			}
			if len(result.ModelCalls) != 1 || result.ModelCalls[0].Reservation != tc.reserve || result.ModelUsage.BudgetTokens != tc.budget || result.ModelUsage.InputTokens != tc.metrics.InputTokens || result.ModelUsage.OutputTokens != tc.metrics.OutputTokens {
				t.Fatalf("cancel released unreceipted output or changed known/unsent policy: %+v %+v", result.ModelCalls, result.ModelUsage)
			}
		})
	}
}
