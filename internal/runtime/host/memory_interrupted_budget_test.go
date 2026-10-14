package host

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type interruptedMemoryBudgetModel struct {
	*memoryInputBudgetModel
}

func (m *interruptedMemoryBudgetModel) ExtractMemoriesMeasured(_ context.Context, request agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, agentcontract.ModelCallMetrics, error) {
	m.inputs = append(m.inputs, request.Text)
	return nil, agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 500}, agentcontract.ModelDecisionError{Kind: "model_outcome_unknown"}
}

func TestInterruptedMemoryCannotReleaseBoundedBudget(t *testing.T) {
	model := &interruptedMemoryBudgetModel{memoryInputBudgetModel: &memoryInputBudgetModel{
		memoryTestModel: &memoryTestModel{hostModel: &hostModel{}},
		measurement:     agentcontract.InputMeasurement{Tokens: 500, Source: "test_measurement"},
	}}
	host, _ := memoryTestHost(t, model.memoryTestModel)
	host.Model = model
	host.Settings.MaxModelTokens = 10000
	run, err := host.Create(t.Context(), "alice", "records", "Use Chinese.", "interrupted-memory-budget")
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
		t.Fatalf("interrupted memory released budget for decision: memory=%d decisions=%d status=%s code=%v", len(model.inputs), model.calls, run.Status, state.ErrorCode)
	}
	if len(state.ModelCalls) != 1 || !state.ModelCalls[0].Reservation || state.ModelCalls[0].UsageAvailable || state.ModelUsage.BudgetTokens != 10000 || state.ModelUsage.InputTokens != 0 || state.ModelUsage.OutputTokens != 0 {
		t.Fatalf("unknown body receipt settled as zero output: calls=%+v usage=%+v", state.ModelCalls, state.ModelUsage)
	}
}
