package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostAuthManagedBrowserBindings(t *testing.T) {
	for _, backend := range []string{"", "records"} {
		t.Run("backend="+backend, func(t *testing.T) {
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				owner := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer host-")
				if owner != "alice" && owner != "bob" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				writeJSON(w, http.StatusOK, JSON{"owner_id": owner})
			}))
			t.Cleanup(auth.Close)
			t.Setenv("HOST_AUTH_URL", auth.URL)
			t.Setenv("WEB_KEY", strings.Repeat("k", 32))
			t.Setenv("ADMIN_KEY", "admin-secret-with-adequate-length")
			t.Setenv("RECORDS_URL", "http://unused.invalid")
			dir := t.TempDir()
			writeFile := func(name string, value any) {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeFile("frontend.json", frontendTestProfile(false))
			web := &WebIntegrationConfig{DatabasePath: "web.sqlite3", BrowserBridge: true, Chat: true, SessionKeyEnv: "WEB_KEY", Integrations: map[string]WebProfileConfig{
				"browser-app": {PackID: backend, FrontendProfilePath: "frontend.json"},
				"sibling":     {PackID: backend, FrontendProfilePath: "frontend.json"},
			}}
			writeFile("deployment.json", DeploymentConfig{Settings: DefaultHostSettings(), DatabasePath: "runs.sqlite3", HostAuth: &HostAuthConfig{URLEnv: "HOST_AUTH_URL"},
				Management: &ManagementConfig{DatabasePath: "registry.sqlite3", PackageDir: "packages", AdminAPIKeyEnv: "ADMIN_KEY"}, WebIntegration: web})
			d, err := LoadDeployment(filepath.Join(dir, "deployment.json"))
			if err != nil {
				t.Fatal(err)
			}
			h := NewAgentHost(testStore(t), d.ProviderFactory, &hostModel{decisions: browserDecisions()}, d.PolicyResolver)
			h.ReleaseResolver, h.ReleaseProviderFactory = d.ReleaseResolver, d.ReleaseProviderFactory
			s, err := NewHTTPServer(h, d, false, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
				if err := d.Registry.Close(); err != nil {
					t.Error(err)
				}
			})
			call := func(method, path, key string, body any) *httptest.ResponseRecorder {
				t.Helper()
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(method, path, bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+key)
				out := httptest.NewRecorder()
				s.Handler().ServeHTTP(out, req)
				return out
			}
			admin := d.Environment["ADMIN_KEY"]
			if backend != "" {
				release, err := d.Registry.Publish(backend, "1.0.0", testRegistryManifest("records.get"), map[string]string{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = d.Registry.Activate(backend, release["digest"].(string), intRef(0)); err != nil {
					t.Fatal(err)
				}
				binding := ConnectionConfig{Environment: map[string]string{"RECORDS_URL": "RECORDS_URL"}, GrantedCapabilities: []string{"records.get"}, AllowModelData: true}
				if out := call("PUT", "/admin/api/bindings/alice/records", admin, binding); out.Code != 200 {
					t.Fatal(out.Code, out.Body.String())
				}
				binding.GrantedCapabilities = append(binding.GrantedCapabilities, "ui.navigate")
				if out := call("PUT", "/admin/api/bindings/alice/records", admin, binding); out.Code != 422 {
					t.Fatal("browser grant accepted in a business binding", out.Code)
				}
			}
			binding := ConnectionConfig{GrantedCapabilities: []string{"ui.navigate"}, ApprovalCapabilities: []string{"ui.navigate"}, AllowModelData: true}
			path := "/admin/api/bindings/alice/browser-app"
			if out := call("PUT", path, "host-alice", binding); out.Code != 401 {
				t.Fatal("host credential accepted as admin", out.Code)
			}
			invalid := binding
			invalid.GrantedCapabilities = []string{"ui.undeclared"}
			if out := call("PUT", path, admin, invalid); out.Code != 422 || !strings.Contains(out.Body.String(), "binding_capability_missing") {
				t.Fatal("unknown browser grant", out.Code, out.Body.String())
			}
			invalid = binding
			invalid.ApprovalCapabilities = []string{"ui.undeclared"}
			if out := call("PUT", path, admin, invalid); out.Code != 422 {
				t.Fatal("unknown browser approval", out.Code)
			}
			invalid = binding
			invalid.Environment = map[string]string{"TOKEN": "ADMIN_KEY"}
			if out := call("PUT", path, admin, invalid); out.Code != 422 {
				t.Fatal("browser policy silently accepted backend credentials", out.Code)
			}
			if out := call("PUT", path, admin, binding); out.Code != 200 {
				t.Fatal("managed browser binding", out.Code, out.Body.String())
			}
			if out := call("POST", path+"/check", admin, nil); out.Code != 200 || !strings.Contains(out.Body.String(), "ui.navigate") {
				t.Fatal("browser connection check", out.Code, out.Body.String())
			}
			policy, err := h.PolicyResolver(t.Context(), "alice", "browser-app")
			if err != nil || !policy.GrantedCapabilities["ui.navigate"] || !policy.ApprovalCapabilities["ui.navigate"] || !policy.AllowModelData {
				t.Fatal("managed browser policy", policy, err)
			}
			if backend != "" && !policy.GrantedCapabilities["records.get"] {
				t.Fatal("backend grants lost")
			}
			if backend != "" {
				business := ConnectionConfig{Environment: map[string]string{"RECORDS_URL": "RECORDS_URL"}, GrantedCapabilities: []string{"records.get"}, AllowModelData: false}
				if out := call("PUT", "/admin/api/bindings/alice/records", admin, business); out.Code != 200 {
					t.Fatal(out.Code, out.Body.String())
				}
				if _, _, err := s.Web.CreateBrowserSession(t.Context(), "alice", "browser-app", "1", []string{"ui.navigate"}); ErrorCode(err) != "model_data_not_authorized" {
					t.Fatal("browser policy bypassed backend consent", err)
				}
				business.AllowModelData = true
				if out := call("PUT", "/admin/api/bindings/alice/records", admin, business); out.Code != 200 {
					t.Fatal(out.Code, out.Body.String())
				}
			}
			if policy, err := h.PolicyResolver(t.Context(), "alice", "sibling"); err == nil && policy.GrantedCapabilities["ui.navigate"] {
				t.Fatal("browser grant leaked to another integration")
			}
			if _, _, err := s.Web.CreateBrowserSession(t.Context(), "bob", "browser-app", "1", []string{"ui.navigate"}); err == nil {
				t.Fatal("alice binding leaked to bob")
			}
			binding.AllowModelData = false
			if out := call("PUT", path, admin, binding); out.Code != 200 {
				t.Fatal(out.Code, out.Body.String())
			}
			if _, _, err := s.Web.CreateBrowserSession(t.Context(), "alice", "browser-app", "1", []string{"ui.navigate"}); ErrorCode(err) != "model_data_not_authorized" {
				t.Fatal("browser consent bypassed", err)
			}
			binding.AllowModelData = true
			if out := call("PUT", path, admin, binding); out.Code != 200 {
				t.Fatal(out.Code, out.Body.String())
			}
			// Exercise the standalone host-token exchange, rather than static users.
			if out := call("POST", "/web/v1/token", "host-alice", JSON{"integration_id": "browser-app"}); out.Code != 200 {
				t.Fatal("dynamic owner ticket", out.Code, out.Body.String())
			}
			session, key, err := s.Web.CreateBrowserSession(t.Context(), "alice", "browser-app", "1", []string{"ui.navigate"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Web.UpdatePageObservation("alice", session.ID, key, 1, 0, JSON{"page": "home"}); err != nil {
				t.Fatal(err)
			}
			run, err := s.Web.CreateBrowserRun(t.Context(), "alice", "browser-app", session.ID, "Open orders", "dynamic-browser-1")
			if err != nil {
				t.Fatal(err)
			}
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "needs_approval" {
				t.Fatal("policy approval skipped", run.Status, err)
			}
			state, err := h.restore(run)
			if err != nil {
				t.Fatal(err)
			}
			item := state.Pending[0]
			if _, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true); err != nil {
				t.Fatal(err)
			}
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "waiting" {
				t.Fatal("approved browser run", run.Status, err)
			}
			commands, blocked, err := s.Web.PollBrowser("alice", session.ID, key, 1)
			if err != nil || blocked || len(commands) != 1 {
				t.Fatal(commands, blocked, err)
			}
			if out := call("DELETE", path, admin, nil); out.Code != 200 {
				t.Fatal(out.Code, out.Body.String())
			}
			if ok, _, err := s.Web.BeginBrowserCommand(t.Context(), "alice", commands[0].ID, key, 1); err == nil || ok {
				t.Fatal("revoked binding executed an already dispatched command", ok, err)
			}
			if out := call("POST", path+"/check", admin, nil); out.Code == 200 {
				t.Fatal("revoked browser binding still passed connection check")
			}
			if out := call("PUT", path, admin, binding); out.Code != 200 {
				t.Fatal(out.Code, out.Body.String())
			}
			if ok, _, err := s.Web.BeginBrowserCommand(t.Context(), "alice", commands[0].ID, key, 1); err != nil || !ok {
				t.Fatal("re-enabled browser binding", ok, err)
			}
			if _, err := s.Web.CompleteBrowserCommand("alice", commands[0].ID, key, 1, "succeeded", JSON{"page": "orders"}, ""); err != nil {
				t.Fatal(err)
			}
			h.Clock = func() float64 { return unixNow() + 2 }
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "completed" {
				t.Fatal("dynamic browser run did not finish", run.Status, err)
			}
			audit, err := d.Registry.Audit(30)
			if err != nil || len(audit) == 0 {
				t.Fatal("browser binding changes lack audit", err)
			}
		})
	}
}

func TestManagedBrowserBindingOverridesStaticActionsAndResolver(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	f.d.Registry = newDraftRegistry(t)
	s := &HTTPServer{Host: f.h, Web: f.w, Deployment: f.d}
	f.w.ResolveBrowserActions = func(context.Context, string, string) ([]string, error) {
		return []string{"ui.navigate"}, nil
	}
	// Use the existing trusted admin handler to select no actions for this alias.
	raw, err := json.Marshal(ConnectionConfig{AllowModelData: true})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", "/admin/api/bindings/alice/records-web", bytes.NewReader(raw))
	out := httptest.NewRecorder()
	s.adminBinding(out, req, "alice/records-web")
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	policy, err := f.w.policy(t.Context(), "alice", "records-web")
	if err != nil || policy.GrantedCapabilities["ui.navigate"] || !policy.GrantedCapabilities["records.get"] {
		t.Fatal("static selection overrode managed browser grants or lost backend grants", policy, err)
	}
	if err := f.d.Registry.DisableBinding("alice", "records-web"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.policy(t.Context(), "alice", "records-web"); ErrorCode(err) != "access_denied" {
		t.Fatal("disabled alias inherited static or hook grants", err)
	}
}
