package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/platform/modelapi"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestTelemetryReadsSnapshotsAndSeparatesReservations(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	h.Settings.MaxModelTokens = 9000
	run := createTestHostRun(t, h)
	state, callErr := h.Restore(run)
	if callErr != nil {
		t.Error(callErr)
	}
	state.ModelCalls = []agentcontract.ModelCallMetrics{{Attempts: 1, UsageAvailable: true, InputTokens: 10, OutputTokens: 2}, {Attempts: 1, EstimatedInputTokens: 30}, {Attempts: 1, Reservation: true, EstimatedInputTokens: 50, ErrorCode: agentcontract.Strptr("model_outcome_unknown")}}
	reactcore.RebuildModelUsage(state)
	runtime, callErr2 := agentcontract.ObjectOf(state)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	run.State["runtime"] = runtime
	run.Status = "running"
	expiry := h.Now() + 30
	run.LeaseUntil = &expiry
	v, err := h.Telemetry(run)
	if err != nil || v.Budget.Tokens.ReportedTokens != 12 || v.Budget.Tokens.EstimatedTokens != 30 || v.Budget.Tokens.ReservedTokens != 50 || *v.Budget.Tokens.Remaining != 8908 {
		t.Fatalf("%+v %v", v, err)
	}
	run.LeaseUntil = nil
	var callErr3 error
	v, callErr3 = h.Telemetry(run)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if v.Budget.Tokens.UnknownTokens != 50 || v.Budget.Tokens.ReservedTokens != 0 {
		t.Fatal(v.Budget)
	}
	h.ProviderFactory = func(context.Context, string, string) (agentcontract.CapabilityProvider, error) {
		t.Fatal("telemetry opened provider")
		return nil, nil
	}
	if _, err = h.GetTelemetry(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err = h.GetTelemetry(t.Context(), run.RunID, "bob"); err == nil {
		t.Fatal("owner isolation")
	}
}

func TestMemoryModelUsageIncludedInRunBudget(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if callErr4 := json.NewDecoder(r.Body).Decode(&payload); callErr4 != nil {
			t.Error(callErr4)
		}
		content := `{"kind":"final","answer_markdown":"hello","fact_ids":[]}`
		if strings.HasPrefix(payload.Messages[0].Content, "Extract enduring") {
			content = `{"proposals":[]}`
		}
		if callErr5 := json.NewEncoder(w).Encode(agentcontract.JSON{"usage": agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 10}, "choices": []any{agentcontract.JSON{"finish_reason": "stop", "message": agentcontract.JSON{"content": content}}}}); callErr5 != nil {
			t.Error(callErr5)
		}
	}))
	defer server.Close()
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	m, callErr6 := modelapi.NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr6 != nil {
		t.Error(callErr6)
	}
	h.Model = m
	h.Settings.MaxModelTokens = 100000
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatalf("%+v %v", run, err)
	}
	v, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
	if err != nil || requests != 2 || v.Budget.Usage.Requests != 2 || v.Budget.Tokens.ReportedTokens != 220 || v.Budget.UsageByPurpose["memory_extraction"].InputTokens != 100 {
		t.Fatalf("%+v %v requests=%d", v, err, requests)
	}
}
