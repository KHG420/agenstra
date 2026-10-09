package service

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
	packregistry "github.com/KHG420/agenstra/internal/state/registry"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func selectionTestDeployment(t *testing.T, endpoint string) *deployassembly.Deployment {
	t.Helper()
	dir := t.TempDir()
	d := &deployassembly.Deployment{BaseDir: dir, Environment: map[string]string{"MODEL_KEY": "synthetic-model-key", "ADMIN_KEY": strings.Repeat("a", 32)}, Config: agentcontract.DeploymentConfig{
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

func TestModelAdminAuthorizationReferencesAndRevision(t *testing.T) {
	d := selectionTestDeployment(t, "https://example.com/v1")
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
	h := durablehost.NewAgentHost(store, nil, m, nil)
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
	if got := request("PUT", d.Environment["ADMIN_KEY"], agentcontract.JSON{"expected_revision": 0, "config": config}); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if got := request("PUT", d.Environment["ADMIN_KEY"], agentcontract.JSON{"expected_revision": 0, "config": config}); got.Code != 409 {
		t.Fatal("revision bypassed", got.Code)
	}
	if got := request("PUT", d.Environment["ADMIN_KEY"], agentcontract.JSON{"expected_revision": 1, "config": config, "api_key": "do-not-store"}); got.Code != 422 {
		t.Fatal("accepted secret or unknown field", got.Code)
	}
}
