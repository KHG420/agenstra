package agenstra

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestAdminAuthAndRevision(t *testing.T) {
	dir := t.TempDir()
	d := &Deployment{BaseDir: dir, Config: DeploymentConfig{DatabasePath: "runs.sqlite3", Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY"}}, Management: &ManagementConfig{DatabasePath: "registry.sqlite3", PackageDir: "packages", AdminAPIKeyEnv: "ADMIN_KEY"}}, Environment: map[string]string{"ALICE_KEY": "alice-secret", "ADMIN_KEY": "this-is-a-distinct-admin-key-123"}}
	d.Registry = NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages"))
	store, e := NewSQLiteStore(filepath.Join(dir, "runs.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(store.Close)
	host := NewAgentHost(store, nil, nil, nil)
	server, e := NewHTTPServer(host, d, false, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(server.Close)
	call := func(method, path, key string, body any) *httptest.ResponseRecorder {
		var b []byte
		if body != nil {
			var callErr error
			b, callErr = json.Marshal(body)
			if callErr != nil {
				t.Error(callErr)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		return w
	}
	for _, userKey := range []string{"", "alice-secret", "synthetic-host-jwt", "synthetic-web-ticket"} {
		for _, endpoint := range []struct{ method, path string }{
			{http.MethodGet, "/admin/api/overview"},
			{http.MethodGet, "/admin/api/models"},
			{http.MethodPut, "/admin/api/models"},
			{http.MethodGet, "/admin/api/drafts"},
			{http.MethodPost, "/admin/api/releases"},
			{http.MethodPut, "/admin/api/bindings/alice/records"},
			{http.MethodGet, "/admin/api/audit"},
		} {
			if w := call(endpoint.method, endpoint.path, userKey, nil); w.Code != http.StatusUnauthorized {
				t.Fatalf("user credential accepted by developer API %s %s: %d", endpoint.method, endpoint.path, w.Code)
			}
		}
	}
	key := d.Environment["ADMIN_KEY"]
	if w := call(http.MethodGet, "/runs", key, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("developer key accepted as a user identity: %d", w.Code)
	}
	if w := call(http.MethodGet, "/admin", "", nil); w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("开发者控制台")) {
		t.Fatalf("framework did not serve its developer console: %d", w.Code)
	}
	manifest := testRegistryManifest("records.get")
	body := map[string]any{"pack_id": "records", "version": "1.0.0", "manifest": manifest, "skills": map[string]string{}}
	w := call("POST", "/admin/api/releases", key, body)
	if w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	var release map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &release); err != nil {
		t.Error(err)
	}
	digest := release["digest"].(string)
	w = call("POST", "/admin/api/packs/records/activate", key, map[string]any{"digest": digest, "expected_revision": 0})
	if w.Code != 200 {
		t.Fatalf("activate: %d %s", w.Code, w.Body.String())
	}
	w = call("POST", "/admin/api/packs/records/activate", key, map[string]any{"digest": digest, "expected_revision": 0})
	if w.Code != 409 {
		t.Fatalf("stale revision: %d %s", w.Code, w.Body.String())
	}
	w = call("PUT", "/admin/api/bindings/alice/records", key, map[string]any{"environment": map[string]string{"RECORDS_URL": "secret:bad-name"}})
	if w.Code != 422 || !bytes.Contains(w.Body.Bytes(), []byte("invalid_environment_ref")) {
		t.Fatalf("invalid environment reference: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/admin/assets/admin_ui.js", "", nil); w.Code != 200 {
		t.Fatalf("asset: %d", w.Code)
	}
}
