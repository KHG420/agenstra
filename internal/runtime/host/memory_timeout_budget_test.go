package host

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type timedOutMemoryBudgetModel struct {
	*memoryInputBudgetModel
	metrics *agentcontract.ModelCallMetrics
}

func (m *timedOutMemoryBudgetModel) ExtractMemoriesMeasured(_ context.Context, request agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, agentcontract.ModelCallMetrics, error) {
	m.inputs = append(m.inputs, request.Text)
	if m.metrics != nil {
		return nil, *m.metrics, context.DeadlineExceeded
	}
	return nil, agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}, context.DeadlineExceeded
}

func TestMemoryTimeoutDoesNotHoldKnownUsageOrUnsentOrUnboundedRequests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   int64
		metrics agentcontract.ModelCallMetrics
	}{
		{name: "reported", limit: 10000, metrics: agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 50, OutputTokens: 20}},
		{name: "not sent", limit: 10000},
		{name: "unbounded", metrics: agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &timedOutMemoryBudgetModel{memoryInputBudgetModel: &memoryInputBudgetModel{
				memoryTestModel: &memoryTestModel{hostModel: &hostModel{}},
				measurement:     agentcontract.InputMeasurement{Tokens: 500, Source: "test_measurement"},
			}, metrics: &tc.metrics}
			host, _ := memoryTestHost(t, model.memoryTestModel)
			host.Model = model
			host.Settings.MaxModelTokens = tc.limit
			run := memoryRun(t, host, "alice", "records", "Use Chinese.", "settled-memory-timeout")
			state, err := host.Restore(run)
			if err != nil {
				t.Fatal(err)
			}
			call := state.ModelCalls[0]
			if model.calls != 1 || call.Reservation || call.UsageAvailable != tc.metrics.UsageAvailable || call.InputTokens != tc.metrics.InputTokens || call.OutputTokens != tc.metrics.OutputTokens || call.Attempts != tc.metrics.Attempts {
				t.Fatalf("known usage or unbounded policy changed: calls=%d metric=%+v", model.calls, call)
			}
		})
	}
}

func TestMemoryTimeoutCannotReleaseUnknownBoundedRequestBudget(t *testing.T) {
	model := &timedOutMemoryBudgetModel{memoryInputBudgetModel: &memoryInputBudgetModel{
		memoryTestModel: &memoryTestModel{hostModel: &hostModel{}},
		measurement:     agentcontract.InputMeasurement{Tokens: 500, Source: "test_measurement"},
	}}
	host, _ := memoryTestHost(t, model.memoryTestModel)
	host.Model = model
	host.Settings.MaxModelTokens = 10000
	run, err := host.Create(t.Context(), "alice", "records", "Use Chinese.", "memory-timeout-budget")
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
	if len(model.inputs) != 1 || model.calls != 0 || run.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "model_token_budget_exhausted" {
		t.Fatalf("unknown memory output released budget for another model request: memory=%d decisions=%d status=%s error=%v", len(model.inputs), model.calls, run.Status, state.ErrorCode)
	}
	if len(state.ModelCalls) != 1 || !state.ModelCalls[0].Reservation || state.ModelCalls[0].UsageAvailable || state.ModelUsage.BudgetTokens != 10000 || state.ModelUsage.InputTokens != 0 || state.ModelUsage.OutputTokens != 0 {
		t.Fatalf("unknown exposure was settled as reported or estimated zero output: %+v", state.ModelUsage)
	}
}
