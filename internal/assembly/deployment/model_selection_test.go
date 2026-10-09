package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
	packregistry "github.com/KHG420/agenstra/internal/state/registry"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func selectionTestDeployment(t *testing.T, endpoint string) *Deployment {
	t.Helper()
	dir := t.TempDir()
	d := &Deployment{BaseDir: dir, Environment: map[string]string{"MODEL_KEY": "synthetic-model-key", "ADMIN_KEY": strings.Repeat("a", 32)}, Config: agentcontract.DeploymentConfig{
		DatabasePath: "runs.sqlite3", Settings: agentcontract.DefaultHostSettings(), Users: map[string]agentcontract.UserConfig{}, Management: &agentcontract.ManagementConfig{DatabasePath: "registry.sqlite3", PackageDir: "packages", AdminAPIKeyEnv: "ADMIN_KEY"},
		Models: &agentcontract.ModelConfiguration{DefaultProfile: "business", MemoryExtractionProfile: "memory", Profiles: map[string]agentcontract.ModelProfile{
			"business": {APIType: "deepseek_chat", Model: "business-old", BaseURL: endpoint, APIKeyRef: "MODEL_KEY", Thinking: "enabled", ReasoningEffort: "low", MaxOutputTokens: 256},
			"memory":   {APIType: "deepseek_chat", Model: "memory-old", BaseURL: endpoint, APIKeyRef: "MODEL_KEY", Thinking: "disabled", MaxOutputTokens: 64},
		}},
	}}
	d.Registry = packregistry.NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages"))
	t.Cleanup(func() {
		if callErr := d.Registry.Close(); callErr != nil {
			t.Error(callErr)
		}
	})
	return d
}

func TestModelSelectionPinsBothPurposesAndSurvivesRestart(t *testing.T) {
	var mu sync.Mutex
	requests := []agentcontract.JSON{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload agentcontract.JSON
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
		message := agentcontract.JSON{"content": content}
		if payload["tools"] != nil {
			message = agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_final", "arguments": `{"answer_markdown":"ok","fact_ids":[]}`}}}}
		}
		if callErr3 := json.NewEncoder(w).Encode(agentcontract.JSON{"usage": agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 10}, "choices": []any{agentcontract.JSON{"message": message}}}); callErr3 != nil {
			t.Error(callErr3)
		}
	}))
	defer upstream.Close()
	d := selectionTestDeployment(t, upstream.URL)
	m, err := d.NewModel()
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.NewSQLiteStore(d.DatabasePath())
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
	h := durablehost.NewAgentHost(store, func(context.Context, string, string) (agentcontract.CapabilityProvider, error) {
		return &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, nil
	}, m, func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{AllowModelData: true}, nil
	})
	oldRun, err := h.Create(t.Context(), "alice", "project", "reply ok", "old")
	if err != nil {
		t.Fatal(err)
	}
	config := m.Snapshot().Config
	for id, profile := range config.Profiles {
		profile.Model = strings.ReplaceAll(profile.Model, "old", "new")
		if id == "business" {
			profile.DecisionOutputMode = "output_tools"
		}
		config.Profiles[id] = profile
	}
	updated, err := m.Configure(config, 0)
	if err != nil || updated.Revision != 1 {
		t.Fatal(updated, err)
	}

	// Returned and input configurations cannot mutate the manager's ownership.
	config.Profiles["memory"] = agentcontract.ModelProfile{}
	if m.Snapshot().Config.Profiles["memory"].Model != "memory-new" {
		t.Fatal("caller mutated catalog")
	}
	newRun, err := h.Create(t.Context(), "alice", "project", "reply ok", "new")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []agentcontract.StoredRun{oldRun, newRun} {
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
	got := append([]agentcontract.JSON{}, requests...)
	mu.Unlock()
	wantModels := []string{"memory-old", "business-old", "memory-new", "business-new"}
	if len(got) != len(wantModels) {
		t.Fatal("unexpected requests", len(got))
	}
	for i, request := range got {
		if request["model"] != wantModels[i] {
			t.Fatal("run rerouted", i, request["model"])
		}
		if (request["tools"] != nil) != (i == 3) {
			t.Fatal("decision output not isolated and pinned", i, request)
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
	if _, err := m.Configure(m.Snapshot().Config, 0); agentcontract.ErrorCode(err) != "model_revision_conflict" {
		t.Fatal("lost update allowed", err)
	}
	if err = d.Registry.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := d.NewModel()
	if err != nil || restarted.Snapshot().Revision != 1 || restarted.Snapshot().Config.Profiles["business"].Model != "business-new" || restarted.Snapshot().Config.Profiles["business"].DecisionOutputMode != "output_tools" {
		t.Fatal("configuration not durable", err)
	}
	raw, callErr4 := agentcontract.CanonicalJSON(oldRun.State)
	if callErr4 != nil {
		t.Error(callErr4)
	}
	if bytes.Contains(raw, []byte(d.Environment["MODEL_KEY"])) {
		t.Fatal("resolved credential persisted")
	}
}

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
