package service

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	packregistry "github.com/KHG420/agenstra/internal/state/registry"
)

func newDraftRegistry(t *testing.T) *packregistry.CapabilityRegistry {
	t.Helper()
	dir := t.TempDir()
	r := packregistry.NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages"))
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func intRef(n int) *int { return &n }

func TestDraftHTTPAuthAndPublication(t *testing.T) {
	r := newDraftRegistry(t)
	deployment := &deployassembly.Deployment{Registry: r, Config: agentcontract.DeploymentConfig{Management: &agentcontract.ManagementConfig{AdminAPIKeyEnv: "ADMIN_KEY"}}, Environment: map[string]string{"ADMIN_KEY": "distinct-admin-key-at-least-24"}}
	server := &HTTPServer{Deployment: deployment}
	call := func(method, path, key string, body any) *httptest.ResponseRecorder {
		var raw []byte
		if body != nil {
			var callErr3 error
			raw, callErr3 = json.Marshal(body)
			if callErr3 != nil {
				t.Error(callErr3)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		server.adminHTTP(w, req)
		return w
	}
	key := deployment.Environment["ADMIN_KEY"]
	if w := call("GET", "/admin/api/drafts", "user-key", nil); w.Code != 401 {
		t.Fatalf("draft auth: %d", w.Code)
	}
	if w := call("PUT", "/admin/api/drafts/draft", key, map[string]any{"expected_revision": 0, "manifest": map[string]any{"schema": "agenstra.rest-pack.v2"}}); w.Code != 200 {
		t.Fatalf("incomplete save: %s", w.Body.String())
	}
	if w := call("POST", "/admin/api/drafts/draft/publish", key, map[string]any{"expected_revision": 1}); w.Code != 422 || !bytes.Contains(w.Body.Bytes(), []byte("issues")) {
		t.Fatalf("incomplete publish: %d %s", w.Code, w.Body.String())
	}
	if w := call("PUT", "/admin/api/drafts/draft", key, map[string]any{"expected_revision": 1, "manifest": testRegistryManifest("records.get")}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("POST", "/admin/api/drafts/draft/publish", key, map[string]any{"expected_revision": 1}); w.Code != 409 {
		t.Fatalf("stale publish: %d", w.Code)
	}
	if w := call("POST", "/admin/api/drafts/draft/publish", key, map[string]any{"expected_revision": 2}); w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	if active, callErr4 := r.ActiveRelease("records"); callErr4 != nil {
		t.Error(callErr4)
	} else if active != "" {
		t.Fatal("publish activated automatically")
	}
	if w := call("GET", "/admin/assets/admin_drafts.js", "", nil); w.Code != 200 {
		t.Fatalf("draft script: %d", w.Code)
	}
	if w := call("PATCH", "/admin/api/drafts/draft", key, map[string]any{"section": "basic", "value": map[string]any{"name": "bad"}}); w.Code != 409 {
		t.Fatalf("missing revision: %d", w.Code)
	}
}
