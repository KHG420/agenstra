package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestHostAuthDynamicOwnersBindingsAndRevocation(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	var hostCalls atomic.Int32
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostCalls.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("host auth method: %s", r.Method)
		}
		owner := ""
		switch r.Header.Get("Authorization") {
		case "Bearer host-alice":
			if active.Load() {
				owner = "alice"
			}
		case "Bearer host-bob":
			owner = "bob"
		}
		if owner == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if callErr := json.NewEncoder(w).Encode(agentcontract.JSON{"owner_id": owner}); callErr != nil {
			t.Error(callErr)
		}
	}))
	defer auth.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, callErr2 := w.Write([]byte(`{"id":"R1"}`)); callErr2 != nil {
			t.Error(callErr2)
		}
	}))
	defer api.Close()
	t.Setenv("HOST_AUTH_URL", auth.URL)
	t.Setenv("ADMIN_KEY", "admin-secret-with-adequate-length")
	t.Setenv("RECORDS_URL", api.URL)
	dir := t.TempDir()
	path := filepath.Join(dir, "deployment.json")
	config := agentcontract.JSON{"database_path": "runs.sqlite3", "host_auth": agentcontract.JSON{"url_env": "HOST_AUTH_URL"}, "management": agentcontract.JSON{"database_path": "registry.sqlite3", "package_dir": "packages", "admin_api_key_env": "ADMIN_KEY"}}
	raw, callErr3 := json.Marshal(config)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	d, err := deployassembly.LoadDeployment(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Registry.Initialize(); err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(d.Registry.Close)
	if owner, err := d.AuthenticateContext(context.Background(), "host-alice"); err != nil || owner != "alice" {
		t.Fatalf("alice host auth: %q %v", owner, err)
	}
	if owner, err := d.AuthenticateContext(context.Background(), "host-bob"); err != nil || owner != "bob" {
		t.Fatalf("bob host auth: %q %v", owner, err)
	}
	if _, err := d.PolicyResolver(context.Background(), "alice", "records"); err == nil {
		t.Fatal("dynamic owner acquired an unbound pack")
	}
	if _, err := d.PolicyResolver(context.Background(), "bob", "records"); err == nil {
		t.Fatal("second owner acquired an unbound pack")
	}
	release, err := d.Registry.Publish("records", "1.0.0", testRegistryManifest("records.get"), map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := d.Registry.Activate("records", release["digest"].(string), &zero); err != nil {
		t.Fatal(err)
	}
	s := &HTTPServer{Deployment: d}
	bind := func(owner string, grants []string) {
		t.Helper()
		body := agentcontract.JSON{"environment": agentcontract.JSON{"RECORDS_URL": "RECORDS_URL"}, "granted_capabilities": grants}
		raw, callErr4 := json.Marshal(body)
		if callErr4 != nil {
			t.Error(callErr4)
		}
		req := httptest.NewRequest(http.MethodPut, "/admin/api/bindings/"+owner+"/records", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer admin-secret-with-adequate-length")
		resp := httptest.NewRecorder()
		s.adminHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("bind %s: status=%d body=%s", owner, resp.Code, resp.Body.String())
		}
	}
	bind("alice", []string{"records.get"})
	alice, err := d.PolicyResolver(context.Background(), "alice", "records")
	if err != nil || !alice.GrantedCapabilities["records.get"] {
		t.Fatalf("alice grant missing: %+v %v", alice, err)
	}
	if _, err := d.PolicyResolver(context.Background(), "bob", "records"); err == nil {
		t.Fatal("alice binding leaked to bob")
	}
	bind("bob", []string{})
	bob, err := d.PolicyResolver(context.Background(), "bob", "records")
	if err != nil || bob.GrantedCapabilities["records.get"] {
		t.Fatalf("bob inherited alice grant: %+v %v", bob, err)
	}
	active.Store(false)
	if _, err := d.AuthenticateContext(context.Background(), "host-alice"); err == nil {
		t.Fatal("revoked token remained valid")
	}
	if owner, err := d.AuthenticateContext(context.Background(), "host-bob"); err != nil || owner != "bob" {
		t.Fatalf("bob affected by alice revocation: %q %v", owner, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	req.Header.Set("Authorization", "Bearer host-bob")
	if owner, err := s.owner(req); err != nil || owner != "bob" {
		t.Fatalf("HTTP owner bridge: %q %v", owner, err)
	}
	if hostCalls.Load() < 4 {
		t.Fatal("host endpoint was not consulted on every dynamic authentication")
	}
}
