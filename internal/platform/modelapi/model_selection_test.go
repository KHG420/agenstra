package modelapi

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestModelParametersRejectUnsupportedAndPreserveMeasurement(t *testing.T) {
	zero := 0.0
	for _, tc := range []struct {
		api, thinking, effort string
		temperature           *float64
		valid                 bool
	}{
		{"compatible_chat", "", "", &zero, true}, {"compatible_chat", "", "low", nil, false},
		{"openai_chat", "", "none", &zero, true}, {"openai_chat", "", "high", &zero, false}, {"openai_chat", "disabled", "", nil, false},
		{"deepseek_chat", "disabled", "", &zero, true}, {"deepseek_chat", "disabled", "low", nil, false}, {"deepseek_chat", "", "", &zero, false}, {"deepseek_chat", "enabled", "max", nil, true}, {"deepseek_chat", "enabled", "medium", nil, false},
	} {
		t.Run(tc.api+"/"+tc.thinking+"/"+tc.effort, func(t *testing.T) {
			if (agentcontract.ValidateModelParameters(tc.api, tc.thinking, tc.effort, tc.temperature) == nil) != tc.valid {
				t.Fatal("parameter support mismatch")
			}
		})
	}
	m, callErr5 := NewHTTPJSONDecisionModel("test", "https://example.com/v1", "test-key", time.Second, nil)
	if callErr5 != nil {
		t.Error(callErr5)
	}
	m.APIType, m.ReasoningEffort, m.MaxOutputTokens, m.TokenLimitField = "openai_chat", "low", 128, "max_completion_tokens"
	payload, err := m.requestPayload([]byte(`{}`), "json")
	if err != nil || payload["reasoning_effort"] != "low" || payload["max_completion_tokens"] != 128 || payload["max_tokens"] != nil {
		t.Fatal(payload, err)
	}
	var measured agentcontract.JSON
	m.CountInputTokens = func(_ string, raw []byte) (int64, error) {
		if callErr6 := json.Unmarshal(raw, &measured); callErr6 != nil {
			t.Error(callErr6)
		}
		return 10, nil
	}
	if _, err := m.MeasureInput(agentcontract.ContextPacket{}, "json"); err != nil || measured["reasoning_effort"] != "low" || measured["max_completion_tokens"] != float64(128) {
		t.Fatal("measurement omitted wire parameters", measured, err)
	}
}

func TestModelUsageBreakdownAndCachePrices(t *testing.T) {
	for _, tc := range []struct {
		name              string
		usage             agentcontract.JSON
		cached, reasoning *int64
		priced            bool
	}{
		{"openai", agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 20, "prompt_tokens_details": agentcontract.JSON{"cached_tokens": 80}, "completion_tokens_details": agentcontract.JSON{"reasoning_tokens": 12}}, int64ptr(80), int64ptr(12), true},
		{"deepseek", agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 20, "prompt_cache_hit_tokens": 80}, int64ptr(80), nil, true},
		{"unknown", agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 20}, nil, nil, false},
		{"invalid", agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 20, "prompt_cache_hit_tokens": 101, "completion_tokens_details": agentcontract.JSON{"reasoning_tokens": -1}}, nil, nil, false},
		{"zero", agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 20, "prompt_cache_hit_tokens": 0, "completion_tokens_details": agentcontract.JSON{"reasoning_tokens": 0}}, int64ptr(0), int64ptr(0), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if callErr7 := json.NewEncoder(w).Encode(agentcontract.JSON{"usage": tc.usage, "choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": `{"kind":"final","answer_markdown":"ok"}`}}}}); callErr7 != nil {
					t.Error(callErr7)
				}
			}))
			defer server.Close()
			m, callErr8 := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
			if callErr8 != nil {
				t.Error(callErr8)
			}
			cachePrice := 0.1
			m.InputPricePerMillion, m.OutputPricePerMillion, m.CachedInputPricePerMillion = 1, 2, &cachePrice
			d, err := m.Decide(t.Context(), agentcontract.ContextPacket{}, "json")
			if err != nil {
				t.Fatal(err)
			}
			metrics := *d.ModelCall
			if !reflect.DeepEqual(metrics.CachedInputTokens, tc.cached) || !reflect.DeepEqual(metrics.ReasoningOutputTokens, tc.reasoning) || (metrics.EstimatedCostUSD != nil) != tc.priced {
				t.Fatal(metrics)
			}
			var usage agentcontract.ModelUsage
			reactcore.AddModelUsage(&usage, metrics)
			if usage.BudgetTokens != 120 || !reflect.DeepEqual(usage.CachedInputTokens, tc.cached) || !reflect.DeepEqual(usage.ReasoningOutputTokens, tc.reasoning) {
				t.Fatal("cache/reasoning changed total budget", usage)
			}
			if tc.cached != nil && *tc.cached == 80 && math.Abs(*metrics.EstimatedCostUSD-0.000068) > 1e-10 {
				t.Fatal("cache price incorrect", *metrics.EstimatedCostUSD)
			}
		})
	}
}

func int64ptr(n int64) *int64 { return &n }
