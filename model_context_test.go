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
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["max_tokens"] != float64(128) {
			t.Fatal(payload)
		}
		_ = json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": `{"kind":"request_input","field":"confirm","prompt":"confirm"}`}}}})
	}))
	defer server.Close()
	m, _ := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
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
	state, _ = r.NewState(strings.Repeat("required", 3000), "")
	_ = r.Step(t.Context(), state, nil)
	if requests != 1 || state.Status != "failed" || !state.ContextTelemetry.OverLimit {
		t.Fatalf("%+v requests=%d", state, requests)
	}
}

func TestUnknownModelWindowIsNotReportedAsZero(t *testing.T) {
	r, state, _ := contextBudgetRuntime(t, 9000)
	r.Model = &coreTestModel{decisions: []Decision{{Kind: "request_input", Field: "x", Prompt: "x"}}}
	_ = r.Step(t.Context(), state, nil)
	c := state.ContextTelemetry
	if c.ModelContextWindowTokens != nil || c.EffectiveInputTokenLimit != nil || c.TokensRemaining != nil || c.InputTokens == nil {
		t.Fatalf("%+v", c)
	}
}
