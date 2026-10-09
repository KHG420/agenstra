package deployment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

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
	d := &Deployment{Config: agentcontract.DeploymentConfig{HostAuth: &agentcontract.HostAuthConfig{URLEnv: "HOST_AUTH_URL"}, Users: map[string]agentcontract.UserConfig{
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
	d.Config.Users["duplicate"] = agentcontract.UserConfig{APIKeyEnv: "DUPLICATE_KEY"}
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
