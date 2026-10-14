package react

import (
	"context"
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type measuredReservationModel struct {
	tokens int64
	calls  int
}

func (m *measuredReservationModel) MeasureInput(agentcontract.ContextPacket, string) (agentcontract.InputMeasurement, error) {
	return agentcontract.InputMeasurement{Tokens: m.tokens, Source: "tokenizer"}, nil
}

func (m *measuredReservationModel) Decide(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
	m.calls++
	return agentcontract.Decision{}, agentcontract.ModelDecisionError{Kind: "model_unavailable"}
}

func TestModelReservationUsesMeasuredRequestInput(t *testing.T) {
	model := &measuredReservationModel{tokens: 50000}
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model}
	state, err := runtime.NewState("Read the current context", "")
	if err != nil {
		t.Fatal(err)
	}
	paused := errors.New("checkpoint observer paused before transport")
	err = runtime.Step(t.Context(), state, func() error {
		call := state.ModelCalls[len(state.ModelCalls)-1]
		if !call.Reservation || call.EstimatedInputTokens != model.tokens || state.ContextTelemetry.InputTokens == nil || *state.ContextTelemetry.InputTokens != model.tokens {
			t.Errorf("checkpoint reservation omitted measured request overhead: call=%#v, measurement=%v", call, state.ContextTelemetry.InputTokens)
		}
		return paused
	})
	if !errors.Is(err, paused) || model.calls != 0 || state.ModelCalls[0].Attempts != 0 {
		t.Fatal("checkpoint failure allowed model transport or recorded an attempted request", err)
	}
}

func TestModelWithoutReturnedMetricsRetainsMeasuredInput(t *testing.T) {
	model := &measuredReservationModel{tokens: 50000}
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model}
	state, err := runtime.NewState("Read the current context", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || len(state.ModelCalls) != 1 || state.ModelCalls[0].EstimatedInputTokens != model.tokens || state.ModelUsage.BudgetTokens < model.tokens {
		t.Fatalf("adapter without returned metrics lost the known input measurement: %#v, %#v", state.ModelCalls, state.ModelUsage)
	}
}
