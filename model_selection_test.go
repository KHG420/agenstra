package agenstra

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func selectionTestDeployment(t *testing.T, endpoint string) *Deployment {
	t.Helper()
	dir := t.TempDir()
	d := &Deployment{BaseDir: dir, Environment: map[string]string{"MODEL_KEY": "synthetic-model-key", "ADMIN_KEY": strings.Repeat("a", 32)}, Config: DeploymentConfig{
		DatabasePath: "runs.sqlite3", Settings: DefaultHostSettings(), Users: map[string]UserConfig{}, Management: &ManagementConfig{DatabasePath: "registry.sqlite3", PackageDir: "packages", AdminAPIKeyEnv: "ADMIN_KEY"},
		Models: &ModelConfiguration{DefaultProfile: "business", MemoryExtractionProfile: "memory", Profiles: map[string]ModelProfile{
			"business": {APIType: "deepseek_chat", Model: "business-old", BaseURL: endpoint, APIKeyRef: "MODEL_KEY", Thinking: "enabled", ReasoningEffort: "low", MaxOutputTokens: 256},
			"memory":   {APIType: "deepseek_chat", Model: "memory-old", BaseURL: endpoint, APIKeyRef: "MODEL_KEY", Thinking: "disabled", MaxOutputTokens: 64},
		}},
	}}
	d.Registry = NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages"))
	t.Cleanup(func() {
		if callErr := d.Registry.Close(); callErr != nil {
			t.Error(callErr)
		}
	})
	return d
}

func TestModelSelectionPinsBothPurposesAndSurvivesRestart(t *testing.T) {
	var mu sync.Mutex
	requests := []JSON{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload JSON
		if callErr2 := json.NewDecoder(r.Body).Decode(&payload); callErr2 != nil {
			t.Error(callErr2)
		}
		mu.Lock()
		requests = append(requests, payload)
		mu.Unlock()
		content := `{"kind":"final","answer_markdown":"ok","fact_ids":[]}`
		if strings.HasPrefix(payload["model"].(string), "memory") {
			content = `{"proposals":[]}`
		}
		if callErr3 := json.NewEncoder(w).Encode(JSON{"usage": JSON{"prompt_tokens": 100, "completion_tokens": 10}, "choices": []any{JSON{"message": JSON{"content": content}}}}); callErr3 != nil {
			t.Error(callErr3)
		}
	}))
	defer upstream.Close()
	d := selectionTestDeployment(t, upstream.URL)
	m, err := d.NewModel()
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(d.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(store.Close)
	if err = store.Initialize(); err != nil {
		t.Fatal(err)
	}
	h := NewAgentHost(store, func(context.Context, string, string) (CapabilityProvider, error) {
		return &coreTestProvider{caps: map[string]CapabilityDescription{}}, nil
	}, m, func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{AllowModelData: true}, nil
	})
	oldRun, err := h.Create(t.Context(), "alice", "project", "reply ok", "old")
	if err != nil {
		t.Fatal(err)
	}
	config := m.Snapshot().Config
	for id, profile := range config.Profiles {
		profile.Model = strings.ReplaceAll(profile.Model, "old", "new")
		config.Profiles[id] = profile
	}
	updated, err := m.Configure(config, 0)
	if err != nil || updated.Revision != 1 {
		t.Fatal(updated, err)
	}
	// Returned and input configurations cannot mutate the manager's ownership.
	config.Profiles["memory"] = ModelProfile{}
	if m.Snapshot().Config.Profiles["memory"].Model != "memory-new" {
		t.Fatal("caller mutated catalog")
	}
	newRun, err := h.Create(t.Context(), "alice", "project", "reply ok", "new")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []StoredRun{oldRun, newRun} {
		result, err := h.Drive(t.Context(), run.RunID, "alice")
		if err != nil || result.Status != "completed" {
			t.Fatal(result.Status, result.State["runtime"], err)
		}
		telemetry, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
		if err != nil || telemetry.Budget.UsageByPurpose["memory_extraction"].Requests != 1 || telemetry.Budget.UsageByPurpose["decision"].Requests != 1 {
			t.Fatal(telemetry, err)
		}
		wantRevision := 0
		if run.RunID == newRun.RunID {
			wantRevision = 1
		}
		if telemetry.EffectiveConfig.ModelSelection.Revision != wantRevision {
			t.Fatal("selection not pinned", telemetry.EffectiveConfig)
		}
	}
	mu.Lock()
	got := append([]JSON{}, requests...)
	mu.Unlock()
	wantModels := []string{"memory-old", "business-old", "memory-new", "business-new"}
	if len(got) != len(wantModels) {
		t.Fatal("unexpected requests", len(got))
	}
	for i, request := range got {
		if request["model"] != wantModels[i] {
			t.Fatal("run rerouted", i, request["model"])
		}
		thinking := request["thinking"].(map[string]any)["type"]
		if i%2 == 0 {
			if thinking != "disabled" || request["reasoning_effort"] != nil || request["max_tokens"] != float64(64) {
				t.Fatal("memory parameters", request)
			}
		} else if thinking != "enabled" || request["reasoning_effort"] != "low" {
			t.Fatal("decision parameters", request)
		}
	}
	if _, err := m.Configure(m.Snapshot().Config, 0); ErrorCode(err) != "model_revision_conflict" {
		t.Fatal("lost update allowed", err)
	}
	if err = d.Registry.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := d.NewModel()
	if err != nil || restarted.Snapshot().Revision != 1 || restarted.Snapshot().Config.Profiles["business"].Model != "business-new" {
		t.Fatal("configuration not durable", err)
	}
	raw, callErr4 := CanonicalJSON(oldRun.State)
	if callErr4 != nil {
		t.Error(callErr4)
	}
	if bytes.Contains(raw, []byte(d.Environment["MODEL_KEY"])) {
		t.Fatal("resolved credential persisted")
	}
}

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
			if (validateModelParameters(tc.api, tc.thinking, tc.effort, tc.temperature) == nil) != tc.valid {
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
	var measured JSON
	m.CountInputTokens = func(_ string, raw []byte) (int64, error) {
		if callErr6 := json.Unmarshal(raw, &measured); callErr6 != nil {
			t.Error(callErr6)
		}
		return 10, nil
	}
	if _, err := m.MeasureInput(ContextPacket{}, "json"); err != nil || measured["reasoning_effort"] != "low" || measured["max_completion_tokens"] != float64(128) {
		t.Fatal("measurement omitted wire parameters", measured, err)
	}
}

func TestModelUsageBreakdownAndCachePrices(t *testing.T) {
	for _, tc := range []struct {
		name              string
		usage             JSON
		cached, reasoning *int64
		priced            bool
	}{
		{"openai", JSON{"prompt_tokens": 100, "completion_tokens": 20, "prompt_tokens_details": JSON{"cached_tokens": 80}, "completion_tokens_details": JSON{"reasoning_tokens": 12}}, int64ptr(80), int64ptr(12), true},
		{"deepseek", JSON{"prompt_tokens": 100, "completion_tokens": 20, "prompt_cache_hit_tokens": 80}, int64ptr(80), nil, true},
		{"unknown", JSON{"prompt_tokens": 100, "completion_tokens": 20}, nil, nil, false},
		{"invalid", JSON{"prompt_tokens": 100, "completion_tokens": 20, "prompt_cache_hit_tokens": 101, "completion_tokens_details": JSON{"reasoning_tokens": -1}}, nil, nil, false},
		{"zero", JSON{"prompt_tokens": 100, "completion_tokens": 20, "prompt_cache_hit_tokens": 0, "completion_tokens_details": JSON{"reasoning_tokens": 0}}, int64ptr(0), int64ptr(0), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if callErr7 := json.NewEncoder(w).Encode(JSON{"usage": tc.usage, "choices": []any{JSON{"message": JSON{"content": `{"kind":"final","answer_markdown":"ok"}`}}}}); callErr7 != nil {
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
			d, err := m.Decide(t.Context(), ContextPacket{}, "json")
			if err != nil {
				t.Fatal(err)
			}
			metrics := *d.ModelCall
			if !reflect.DeepEqual(metrics.CachedInputTokens, tc.cached) || !reflect.DeepEqual(metrics.ReasoningOutputTokens, tc.reasoning) || (metrics.EstimatedCostUSD != nil) != tc.priced {
				t.Fatal(metrics)
			}
			var usage ModelUsage
			addModelUsage(&usage, metrics)
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

func TestModelProtocolCheckRejectsSuffixAndDoesNotRetry(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"valid", `{"kind":"final","answer_markdown":"ok"}`, ""},
		{"suffix", `{"kind":"final","answer_markdown":"ok"}</｜DSML｜calls>`, "model_output_protocol_mismatch"},
		{"empty", "", "model_output_empty"}, {"json", "not json", "model_output_invalid_json"},
		{"schema", `{"kind":"final","answer_markdown":"ok","invented":true}`, "model_decision_schema_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; memoryModelReply(t, w, tc.content) }))
			defer server.Close()
			m, err := selectionTestDeployment(t, server.URL).NewModel()
			if err != nil {
				t.Fatal(err)
			}
			result, err := m.Check(t.Context(), "business", "decision", nil)
			if err != nil || result.Passed != (tc.want == "") || result.ErrorCode != tc.want || requests != 1 {
				t.Fatal(result, requests, err)
			}
		})
	}
}

func TestModelAdminAuthorizationReferencesAndRevision(t *testing.T) {
	d := selectionTestDeployment(t, "https://example.com/v1")
	m, err := d.NewModel()
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(d.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(store.Close)
	h := NewAgentHost(store, nil, m, nil)
	s, err := NewHTTPServer(h, d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(s.Close)
	request := func(method, key string, body any) *httptest.ResponseRecorder {
		raw, callErr9 := json.Marshal(body)
		if callErr9 != nil {
			t.Error(callErr9)
		}
		req := httptest.NewRequest(method, "/admin/api/models", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	if got := request("GET", "wrong", nil); got.Code != 401 {
		t.Fatal(got.Code)
	}
	got := request("GET", d.Environment["ADMIN_KEY"], nil)
	if got.Code != 200 || strings.Contains(got.Body.String(), d.Environment["MODEL_KEY"]) || !strings.Contains(got.Body.String(), "MODEL_KEY") {
		t.Fatal("credential response", got.Code, got.Body.String())
	}
	config := m.Snapshot().Config
	if got := request("PUT", d.Environment["ADMIN_KEY"], JSON{"expected_revision": 0, "config": config}); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if got := request("PUT", d.Environment["ADMIN_KEY"], JSON{"expected_revision": 0, "config": config}); got.Code != 409 {
		t.Fatal("revision bypassed", got.Code)
	}
	if got := request("PUT", d.Environment["ADMIN_KEY"], JSON{"expected_revision": 1, "config": config, "api_key": "do-not-store"}); got.Code != 422 {
		t.Fatal("accepted secret or unknown field", got.Code)
	}
}
