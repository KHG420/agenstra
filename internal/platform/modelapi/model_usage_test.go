package modelapi

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestModelUsageTracksRetriesOutputLimitAndPrice(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload agentcontract.JSON
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
		if callErr4 := json.NewEncoder(w).Encode(agentcontract.JSON{"usage": agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 20}, "choices": []any{agentcontract.JSON{"finish_reason": "stop", "message": agentcontract.JSON{"content": `{"kind":"final","answer_markdown":"hello","fact_ids":[]}`}}}}); callErr4 != nil {
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
	r := &reactcore.AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: m}
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
	raw, callErr6 := agentcontract.CanonicalJSON(result.Decisions)
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
		if callErr7 := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": `{"kind":"final","answer_markdown":"hello","fact_ids":[]}`}}}}); callErr7 != nil {
			t.Error(callErr7)
		}
	}))
	defer server.Close()
	m, callErr8 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr8 != nil {
		t.Error(callErr8)
	}
	r := &reactcore.AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: m}
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
