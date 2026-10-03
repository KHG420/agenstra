package agenstra

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestModelContextWindowReservesOutputAndCountsSerializedInput(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload JSON
		if callErr := json.NewDecoder(r.Body).Decode(&payload); callErr != nil {
			t.Error(callErr)
		}
		if payload["max_tokens"] != float64(128) {
			t.Fatal(payload)
		}
		if callErr2 := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": `{"kind":"request_input","field":"confirm","prompt":"confirm"}`}}}}); callErr2 != nil {
			t.Error(callErr2)
		}
	}))
	defer server.Close()
	m, callErr3 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr3 != nil {
		t.Error(callErr3)
	}
	m.CountInputTokens = func(_ string, raw []byte) (int64, error) {
		var payload JSON
		if json.Unmarshal(raw, &payload) != nil || payload["messages"] == nil {
			t.Fatal("not full payload")
		}
		return int64(len(raw) / 4), nil
	}
	r, state, _ := contextBudgetRuntime(t, 30000)
	r.Model = m
	r.ModelContextWindowTokens = 2500
	r.ModelOutputReserveTokens = 128
	r.ModelProtocolReserveTokens = 64
	for i := 0; i < 12; i++ {
		state.Facts = append(state.Facts, contextBudgetFact(JSON{"body": strings.Repeat("x", 1500)}))
	}
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	c := state.ContextTelemetry
	if requests != 1 || c.TokenMeasurementSource != "tokenizer" || c.InputTokens == nil || *c.InputTokens > 2308 || *c.EffectiveInputTokenLimit != 2308 || *c.ReservedOutputTokens != 128 || c.TokensRemaining == nil {
		t.Fatalf("%+v requests=%d", c, requests)
	}
	var callErr4 error
	state, callErr4 = r.NewState(strings.Repeat("required", 3000), "")
	if callErr4 != nil {
		t.Error(callErr4)
	}
	if callErr5 := r.Step(t.Context(), state, nil); callErr5 != nil {
		t.Error(callErr5)
	}
	if requests != 1 || state.Status != "failed" || !state.ContextTelemetry.OverLimit {
		t.Fatalf("%+v requests=%d", state, requests)
	}
}

func TestUnknownModelWindowIsNotReportedAsZero(t *testing.T) {
	r, state, _ := contextBudgetRuntime(t, 9000)
	r.Model = &coreTestModel{decisions: []Decision{{Kind: "request_input", Field: "x", Prompt: "x"}}}
	if callErr6 := r.Step(t.Context(), state, nil); callErr6 != nil {
		t.Error(callErr6)
	}
	c := state.ContextTelemetry
	if c.ModelContextWindowTokens != nil || c.EffectiveInputTokenLimit != nil || c.TokensRemaining != nil || c.InputTokens == nil {
		t.Fatalf("%+v", c)
	}
}
