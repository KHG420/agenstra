package host

import (
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type memoryInputBudgetModel struct {
	*memoryTestModel
	measurement agentcontract.InputMeasurement
	measureErr  error
	measured    int
	request     agentcontract.MemoryExtractionRequest
}

func (m *memoryInputBudgetModel) MeasureMemoryInput(request agentcontract.MemoryExtractionRequest) (agentcontract.InputMeasurement, error) {
	m.measured++
	m.request = request
	return m.measurement, m.measureErr
}

func TestMemoryInputMeasurementIsReservedBeforeExtraction(t *testing.T) {
	model := &memoryInputBudgetModel{
		memoryTestModel: &memoryTestModel{hostModel: &hostModel{}},
		measurement:     agentcontract.InputMeasurement{Tokens: 18000, Source: "test_measurement"},
	}
	host, store := memoryTestHost(t, model.memoryTestModel)
	host.Model = model
	host.Settings.MaxModelOutputTokens = 4096
	host.Settings.ModelOutputReserveTokens = 1024
	var run agentcontract.StoredRun
	model.extract = func(agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
		saved, err := store.GetRun(run.RunID, "alice")
		if err != nil {
			t.Fatal(err)
		}
		state, err := host.Restore(saved)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.ModelCalls) != 1 || !state.ModelCalls[0].Reservation || state.ModelCalls[0].EstimatedInputTokens != 18000 {
			t.Fatalf("memory request did not persist measured input before dispatch: %+v", state.ModelCalls)
		}
		return nil, nil
	}
	var err error
	run, err = host.Create(t.Context(), "alice", "records", "Use Chinese.", "measured-memory-input")
	if err != nil {
		t.Fatal(err)
	}
	run, err = host.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || model.measured != 1 || model.request.MaxOutputTokens != 1024 {
		t.Fatalf("run=%s err=%v measurements=%d output=%d", run.Status, err, model.measured, model.request.MaxOutputTokens)
	}
	state, err := host.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	if state.ModelCalls[0].EstimatedInputTokens != 18000 || state.ModelCalls[0].Reservation {
		t.Fatal("unmeasured extractor lost the measured input estimate", state.ModelCalls[0])
	}
}

func TestMemoryInputMeasurementBlocksOverspendOrFailureBeforeExtraction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int64
		err   error
	}{
		{name: "budget", limit: 10000},
		{name: "measurement failure", err: errors.New("private measurement failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &memoryInputBudgetModel{
				memoryTestModel: &memoryTestModel{hostModel: &hostModel{}, extract: func(agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) { return nil, nil }},
				measurement:     agentcontract.InputMeasurement{Tokens: 18000, Source: "test_measurement"},
				measureErr:      tc.err,
			}
			host, _ := memoryTestHost(t, model.memoryTestModel)
			host.Model = model
			host.Settings.MaxModelTokens = tc.limit
			memoryRun(t, host, "alice", "records", "Use Chinese.", "blocked-memory-input")
			if model.measured != 1 || len(model.inputs) != 0 {
				t.Fatalf("extraction bypassed input preflight: measurements=%d inputs=%d", model.measured, len(model.inputs))
			}
		})
	}
}
