package host

import (
	"context"
	"errors"
	"fmt"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type cancelledMemoryBudgetModel struct {
	*memoryInputBudgetModel
	metrics agentcontract.ModelCallMetrics
	failure error
}

func (m *cancelledMemoryBudgetModel) ExtractMemoriesMeasured(_ context.Context, request agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, agentcontract.ModelCallMetrics, error) {
	m.inputs = append(m.inputs, request.Text)
	failure := m.failure
	if failure == nil {
		failure = fmt.Errorf("extractor-local cancellation: %w", context.Canceled)
	}
	return nil, m.metrics, failure
}

func TestCancelledMemoryCannotReleaseUnknownBoundedBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   int64
		metrics agentcontract.ModelCallMetrics
		reserve bool
		failure error
	}{
		{name: "dispatched unknown", limit: 10000, metrics: agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}, reserve: true},
		{name: "valid usage", limit: 10000, metrics: agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 50, OutputTokens: 20}},
		{name: "unsent", limit: 10000},
		{name: "unbounded", metrics: agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}},
		{name: "deliberate code wins", limit: 10000, metrics: agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}, failure: errors.Join(agentcontract.ModelDecisionError{Kind: "model_refused"}, context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &cancelledMemoryBudgetModel{memoryInputBudgetModel: &memoryInputBudgetModel{memoryTestModel: &memoryTestModel{hostModel: &hostModel{}}, measurement: agentcontract.InputMeasurement{Tokens: 500, Source: "test_measurement"}}, metrics: tc.metrics, failure: tc.failure}
			host, _ := memoryTestHost(t, model.memoryTestModel)
			host.Model = model
			host.Settings.MaxModelTokens = tc.limit
			run, err := host.Create(t.Context(), "alice", "records", "Use Chinese.", "cancelled-memory-budget")
			if err != nil {
				t.Fatal(err)
			}
			run, err = host.Drive(t.Context(), run.RunID, "alice")
			if err != nil {
				t.Fatal(err)
			}
			state, err := host.Restore(run)
			if err != nil {
				t.Fatal(err)
			}
			call := state.ModelCalls[0]
			if call.Reservation != tc.reserve || call.UsageAvailable != tc.metrics.UsageAvailable || call.InputTokens != tc.metrics.InputTokens || call.OutputTokens != tc.metrics.OutputTokens || call.Attempts != tc.metrics.Attempts {
				t.Fatalf("cancellation changed unknown/known/unsent settlement: %+v", call)
			}
			if tc.reserve {
				if model.calls != 0 || run.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "model_token_budget_exhausted" || state.ModelUsage.BudgetTokens != 10000 {
					t.Fatalf("cancelled extraction released budget for decision: model_calls=%d status=%s usage=%+v", model.calls, run.Status, state.ModelUsage)
				}
			} else if model.calls != 1 {
				t.Fatalf("changed known, unsent or unbounded extraction policy: decision calls=%d", model.calls)
			}
		})
	}
}
