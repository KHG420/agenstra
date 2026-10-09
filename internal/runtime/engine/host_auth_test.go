package engine

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
		if callErr := json.NewEncoder(w).Encode(JSON{"owner_id": owner}); callErr != nil {
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
	config := JSON{"database_path": "runs.sqlite3", "host_auth": JSON{"url_env": "HOST_AUTH_URL"}, "management": JSON{"database_path": "registry.sqlite3", "package_dir": "packages", "admin_api_key_env": "ADMIN_KEY"}}
	raw, callErr3 := json.Marshal(config)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	d, err := LoadDeployment(path)
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
		body := JSON{"environment": JSON{"RECORDS_URL": "RECORDS_URL"}, "granted_capabilities": grants}
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

func TestHostAuthRejectsMalformedRedirectAndAmbiguousStaticKeys(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer redirect":
			http.Redirect(w, r, target.URL, http.StatusFound)
		case "Bearer malformed":
			if _, callErr5 := w.Write([]byte(`{"owner_id":`)); callErr5 != nil {
				t.Error(callErr5)
			}
		case "Bearer missing":
			if _, callErr6 := w.Write([]byte(`{"other":"alice"}`)); callErr6 != nil {
				t.Error(callErr6)
			}
		case "Bearer empty":
			if _, callErr7 := w.Write([]byte(`{"owner_id":" "}`)); callErr7 != nil {
				t.Error(callErr7)
			}
		case "Bearer oversized":
			if _, callErr8 := w.Write([]byte(`{"owner_id":"` + strings.Repeat("x", 1<<20) + `"}`)); callErr8 != nil {
				t.Error(callErr8)
			}
		case "Bearer trailing":
			if _, callErr9 := w.Write([]byte(`{"owner_id":"alice"}{"owner_id":"bob"}`)); callErr9 != nil {
				t.Error(callErr9)
			}
		case "Bearer collide":
			if _, callErr10 := w.Write([]byte(`{"owner_id":"static"}`)); callErr10 != nil {
				t.Error(callErr10)
			}
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer auth.Close()
	d := &Deployment{Config: DeploymentConfig{HostAuth: &HostAuthConfig{URLEnv: "HOST_AUTH_URL"}, Users: map[string]UserConfig{
		"static": {APIKeyEnv: "STATIC_KEY"},
	}}, Environment: map[string]string{"HOST_AUTH_URL": auth.URL, "STATIC_KEY": "static-token"}, IdentityClient: auth.Client()}
	for _, token := range []string{"redirect", "malformed", "missing", "empty", "oversized", "trailing", "collide", "unknown"} {
		if owner, err := d.AuthenticateContext(context.Background(), token); err == nil || owner != "" {
			t.Fatalf("accepted %s: %q %v", token, owner, err)
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("host auth followed redirect and forwarded bearer token")
	}
	if owner, err := d.Authenticate("static-token"); err != nil || owner != "static" {
		t.Fatalf("static auth compatibility: %q %v", owner, err)
	}
	d.Config.Users["duplicate"] = UserConfig{APIKeyEnv: "DUPLICATE_KEY"}
	d.Environment["DUPLICATE_KEY"] = "static-token"
	if owner, err := d.AuthenticateContext(context.Background(), "static-token"); err == nil || owner != "" {
		t.Fatalf("ambiguous static token fell through to host auth: %q %v", owner, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.AuthenticateContext(ctx, "redirect"); err == nil {
		t.Fatal("cancelled authentication succeeded")
	}
}

func TestHostAuthDeploymentRequiresManagedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployment.json")
	for _, body := range []string{
		`{"database_path":"runs.sqlite3","host_auth":{"url_env":"HOST_AUTH_URL"}}`,
		`{"database_path":"runs.sqlite3","host_auth":{"url_env":"bad-name"},"management":{"database_path":"registry.sqlite3","package_dir":"packages","admin_api_key_env":"ADMIN_KEY"}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDeployment(path); err == nil {
			t.Fatalf("accepted unsafe deployment: %s", body)
		}
	}
}
