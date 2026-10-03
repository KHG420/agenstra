package agenstra

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestModelTokenReservationSurvivesLostResponseCheckpoint(t *testing.T) {
	m := &coreTestModel{decisions: []Decision{{Kind: "final", AnswerMarkdown: "hello"}}}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: m, MaxModelTokens: 5000}
	state, callErr := r.NewState("hello", "")
	if callErr != nil {
		t.Error(callErr)
	}
	var checkpoint []byte
	err := r.Step(t.Context(), state, func() error {
		var callErr2 error
		checkpoint, callErr2 = CanonicalJSON(state)
		if callErr2 != nil {
			t.Error(callErr2)
		}
		return errors.New("simulate crash before response checkpoint")
	})
	if err == nil || m.calls != 0 {
		t.Fatal("unexpected model call")
	}
	var restored RuntimeState
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

func TestModelUsageTracksRetriesOutputLimitAndPrice(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload JSON
		if callErr3 := json.NewDecoder(r.Body).Decode(&payload); callErr3 != nil {
			t.Error(callErr3)
		}
		if payload["max_tokens"] != float64(512) {
			t.Fatalf("limit: %+v", payload)
		}
		if requests == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("X-Request-ID", "request-42")
		if callErr4 := json.NewEncoder(w).Encode(JSON{"usage": JSON{"prompt_tokens": 100, "completion_tokens": 20}, "choices": []any{JSON{"finish_reason": "stop", "message": JSON{"content": `{"kind":"final","answer_markdown":"hello","fact_ids":[]}`}}}}); callErr4 != nil {
			t.Error(callErr4)
		}
	}))
	defer server.Close()
	m, callErr5 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr5 != nil {
		t.Error(callErr5)
	}
	m.MaxOutputTokens = 512
	m.InputPricePerMillion = 1
	m.OutputPricePerMillion = 2
	m.RetryBaseDelay = time.Nanosecond
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: m}
	result, err := r.Run(t.Context(), "hello")
	if err != nil || result.Status != "completed" || len(result.ModelCalls) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	call := result.ModelCalls[0]
	if !call.UsageAvailable || call.Attempts != 2 || call.RequestID != "request-42" || call.FinishReason != "stop" || call.Round != 1 || result.ModelUsage.InputTokens != 100 || result.ModelUsage.OutputTokens != 20 {
		t.Fatalf("%+v %+v", call, result.ModelUsage)
	}
	if call.EstimatedCostUSD == nil || math.Abs(*call.EstimatedCostUSD-.00014) > 1e-10 {
		t.Fatalf("cost=%v", call.EstimatedCostUSD)
	}
	if result.ModelUsage.EstimatedRequests != 1 || result.ModelUsage.Requests != 2 {
		t.Fatalf("%+v", result.ModelUsage)
	}
	raw, callErr6 := CanonicalJSON(result.Decisions)
	if callErr6 != nil {
		t.Error(callErr6)
	}
	if strings.Contains(string(raw), "model_call") || strings.Contains(string(raw), "request-42") {
		t.Fatal("adapter metadata leaked into decision contract")
	}
}

func TestModelUsageUnknownIsEstimatedAndBudgetStopsBeforeIO(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if callErr7 := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": `{"kind":"final","answer_markdown":"hello","fact_ids":[]}`}}}}); callErr7 != nil {
			t.Error(callErr7)
		}
	}))
	defer server.Close()
	m, callErr8 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr8 != nil {
		t.Error(callErr8)
	}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: m}
	result, err := r.Run(t.Context(), "hello")
	if err != nil || result.ModelCalls[0].UsageAvailable || result.ModelUsage.EstimatedRequests != 1 || result.ModelUsage.BudgetTokens <= 0 || result.ModelUsage.CostAvailable {
		t.Fatalf("%+v %v", result, err)
	}
	r.MaxModelTokens = 1
	result, err = r.Run(t.Context(), "hello")
	if err != nil || result.Status != "failed" || *result.ErrorCode != "model_token_budget_exhausted" || requests != 1 || result.ModelUsage.Requests != 0 {
		t.Fatalf("%+v %v requests=%d", result, err, requests)
	}
}

func TestModelUsageOverBudgetCannotExecuteToolDecision(t *testing.T) {
	p := &coreTestProvider{caps: map[string]CapabilityDescription{"records.get": {Name: "records.get", Effect: "read"}}}
	m := decisionModelFunc(func(context.Context, ContextPacket, string) (Decision, error) {
		d := callDecision("records.get")
		d.ModelCall = &ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 900, OutputTokens: 200}
		return d, nil
	})
	r := &AgentRuntime{Provider: p, Model: m, MaxModelTokens: 1000, Grants: map[string]bool{"records.get": true}}
	result, err := r.Run(t.Context(), "lookup")
	if err != nil || result.Status != "failed" || *result.ErrorCode != "model_token_budget_exhausted" || p.called != 0 {
		t.Fatalf("%+v %v calls=%d", result, err, p.called)
	}
}

func TestHostPersistsModelMetricsAndEvent(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.restore(run)
	if err != nil || len(state.ModelCalls) != 1 || state.ModelUsage.Requests != 1 {
		t.Fatalf("%+v %v", state, err)
	}
	events, err := h.Store.ListEvents(run.RunID, "alice", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		payload := event["event"].(JSON)
		if payload["kind"] == "model_decided" {
			_, found = payload["metrics"]
		}
	}
	if !found {
		t.Fatal("model metrics missing from event")
	}
}
