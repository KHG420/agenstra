package react

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestModelTokenReservationSurvivesLostResponseCheckpoint(t *testing.T) {
	m := &coreTestModel{decisions: []agentcontract.Decision{{Kind: "final", AnswerMarkdown: "hello"}}}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: m, MaxModelTokens: 5000}
	state, callErr := r.NewState("hello", "")
	if callErr != nil {
		t.Error(callErr)
	}
	var checkpoint []byte
	err := r.Step(t.Context(), state, func() error {
		var callErr2 error
		checkpoint, callErr2 = agentcontract.CanonicalJSON(state)
		if callErr2 != nil {
			t.Error(callErr2)
		}
		return errors.New("simulate crash before response checkpoint")
	})
	if err == nil || m.calls != 0 {
		t.Fatal("unexpected model call")
	}
	var restored agentcontract.RuntimeState
	if err = json.Unmarshal(checkpoint, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ModelUsage.BudgetTokens != 5000 || *restored.ModelCalls[0].ErrorCode != "model_outcome_unknown" {
		t.Fatalf("%+v", restored)
	}
	if err = r.Step(t.Context(), &restored, nil); err != nil || restored.Status != "failed" || *restored.ErrorCode != "model_token_budget_exhausted" || m.calls != 0 {
		t.Fatalf("%+v %v", restored, err)
	}
}

func TestModelUsageOverBudgetCannotExecuteToolDecision(t *testing.T) {
	p := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {Name: "records.get", Effect: "read"}}}
	m := decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
		d := callDecision("records.get")
		d.ModelCall = &agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 900, OutputTokens: 200}
		return d, nil
	})
	r := &AgentRuntime{Provider: p, Model: m, MaxModelTokens: 1000, Grants: map[string]bool{"records.get": true}}
	result, err := r.Run(t.Context(), "lookup")
	if err != nil || result.Status != "failed" || *result.ErrorCode != "model_token_budget_exhausted" || p.called != 0 {
		t.Fatalf("%+v %v calls=%d", result, err, p.called)
	}
}
