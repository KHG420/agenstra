package agenstra

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowserOnlyIntegrationUsesExplicitConnectionPolicy(t *testing.T) {
	dir := t.TempDir()
	raw, callErr := json.Marshal(frontendTestProfile(true))
	if callErr != nil {
		t.Error(callErr)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{decisions: browserDecisions()})
	cfg := WebIntegrationConfig{DatabasePath: "web.sqlite3", BrowserBridge: true, Chat: true, SessionKeyEnv: "WEB_KEY", Integrations: map[string]WebProfileConfig{"app": {FrontendProfilePath: "frontend.json"}}}
	config := DeploymentConfig{Settings: DefaultHostSettings(), DatabasePath: h.Store.Path, WebIntegration: &cfg, Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY", Packs: map[string]ConnectionConfig{"app": {AllowModelData: true, GrantedCapabilities: []string{"ui.navigate"}}}}}}
	var callErr2 error
	raw, callErr2 = json.Marshal(config)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	path := filepath.Join(dir, "deployment.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEB_KEY", strings.Repeat("k", 32))
	d, err := LoadDeployment(path)
	if err != nil {
		t.Fatal(err)
	}
	h.PolicyResolver = d.PolicyResolver
	h.ProviderFactory = func(context.Context, string, string) (CapabilityProvider, error) {
		return nil, errors.New("browser-only must not open a business pack")
	}
	h.ReleaseProviderFactory = nil
	h.ReleaseResolver = func(context.Context, string, string) (string, error) {
		t.Fatal("browser-only must not resolve a business release")
		return "", nil
	}
	server, err := NewHTTPServer(h, d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	w := server.Web
	policy, err := w.policy(t.Context(), "alice", "app")
	if err != nil || !policy.AllowModelData || !policy.GrantedCapabilities["ui.navigate"] {
		t.Fatalf("policy: %+v %v", policy, err)
	}
	if _, err = w.policy(t.Context(), "other", "app"); ErrorCode(err) != "access_denied" {
		t.Fatal("unknown owner", err)
	}
	session, key, err := w.CreateBrowserSession(t.Context(), "alice", "app", "1", []string{"ui.navigate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.UpdatePageObservation("alice", session.ID, key, 1, 0, JSON{"page": "home"}); err != nil {
		t.Fatal(err)
	}
	run, err := w.CreateBrowserRun(t.Context(), "alice", "app", session.ID, "Open orders", "browser-only-1")
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_approval" {
		t.Fatalf("approval required: %s %v", run.Status, err)
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
		t.Fatalf("approved: %s %v", run.Status, err)
	}
	commands, blocked, err := w.PollBrowser("alice", session.ID, key, 1)
	if err != nil || blocked || len(commands) != 1 {
		t.Fatal(commands, blocked, err)
	}
	if ok, _, err := w.BeginBrowserCommand(t.Context(), "alice", commands[0].ID, key, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err = w.CompleteBrowserCommand("alice", commands[0].ID, key, 1, "succeeded", JSON{"page": "orders"}, ""); err != nil {
		t.Fatal(err)
	}
	h.Clock = func() float64 { return unixNow() + 2 }
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatalf("completed: %s %v", run.Status, err)
	}
	release, err := w.release(t.Context(), "alice", "app")
	if err != nil {
		t.Fatal(err)
	}
	// New profile versions must not change the contract pinned to an existing run.
	w.profiles["app"].profile.Version = "2"
	provider, err := w.releaseProvider(t.Context(), "alice", "app", release)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(provider.Close)
	if !strings.Contains(provider.SystemPrompt(), "agenstra.decision.v1") || !strings.Contains(provider.SystemPrompt(), "tool_batch") {
		t.Fatal("browser-only provider must include the framework decision protocol")
	}
	if provider.Capabilities()["ui.navigate"].Version != "1" {
		t.Fatal("release was not pinned")
	}
	user := d.Config.Users["alice"]
	user.Packs["app"] = ConnectionConfig{AllowModelData: false}
	policy, err = w.policy(t.Context(), "alice", "app")
	if err != nil || policy.AllowModelData || policy.GrantedCapabilities["ui.navigate"] {
		t.Fatalf("revocation: %+v %v", policy, err)
	}
	if _, _, err = w.CreateBrowserSession(t.Context(), "alice", "app", "1", []string{"ui.navigate"}); ErrorCode(err) != "model_data_not_authorized" {
		t.Fatal("consent", err)
	}
}

func TestBrowserAliasCannotChangeIntegrationModeOnRestart(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	// An already persisted combined release cannot be changed into frontend-only.
	cfg := *f.d.Config.WebIntegration
	cfg.Integrations = map[string]WebProfileConfig{"records-web": {FrontendProfilePath: "frontend.json"}}
	h := testHost(t, f.h.Store, f.p, &hostModel{})
	if w, err := NewWebIntegration(h, f.d, cfg); err == nil {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("existing alias silently changed its business identity")
	}
}

// The live evaluation uses only synthetic page handlers. It is opt-in because
// it sends model requests; CI and ordinary tests require no gateway credentials.
func TestLiveBrowserOnlyIntegration(t *testing.T) {
	if os.Getenv("AGENSTRA_LIVE_EVAL") != "1" {
		t.Skip("set AGENSTRA_LIVE_EVAL=1 and AGENT_MODEL credentials")
	}
	model, err := NewHTTPJSONDecisionModel(os.Getenv("AGENT_MODEL"), os.Getenv("AGENT_MODEL_BASE_URL"), os.Getenv("AGENT_MODEL_API_KEY"), 30*time.Second, nil)
	if err != nil {
		t.Fatal("live evaluation requires AGENT_MODEL credentials")
	}
	dir := t.TempDir()
	raw, callErr3 := json.Marshal(frontendTestProfile(false))
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if err = os.WriteFile(filepath.Join(dir, "frontend.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := WebIntegrationConfig{DatabasePath: "web.sqlite3", Chat: true, BrowserBridge: true, SessionKeyEnv: "WEB_KEY", Integrations: map[string]WebProfileConfig{"app": {FrontendProfilePath: "frontend.json"}}}
	d := &Deployment{BaseDir: dir, Environment: map[string]string{"WEB_KEY": strings.Repeat("k", 32)}, Config: DeploymentConfig{Users: map[string]UserConfig{"alice": {Packs: map[string]ConnectionConfig{"app": {AllowModelData: true, GrantedCapabilities: []string{"ui.navigate"}}}}}, WebIntegration: &cfg}}
	h := NewAgentHost(testStore(t), func(context.Context, string, string) (CapabilityProvider, error) {
		t.Fatal("opened business pack")
		return nil, nil
	}, model, d.PolicyResolver)
	d.Config.DatabasePath = h.Store.Path
	// The action Fact is the initial queued receipt. The authoritative completed
	// receipt is produced by the operation's ui.command_status poll.
	evidence, callErr4 := RequireFactValues(
		FactRequirement{Capability: "ui.command_status", Path: []any{"data", "status"}, Value: "succeeded"},
		FactRequirement{Capability: "ui.command_status", Path: []any{"data", "result", "page"}, Value: "orders"},
	)
	if callErr4 != nil {
		t.Error(callErr4)
	}
	h.CompletionValidator = func(ctx context.Context, result CompletionContext) error {
		if err := evidence(ctx, result); err != nil {
			return err
		}
		if !strings.Contains(strings.ToLower(result.AnswerMarkdown), "orders") {
			return CompletionValidationError{"answer_value_mismatch", "Report the confirmed orders page from the completed receipt."}
		}
		return nil
	}
	w, err := NewWebIntegration(h, d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(w.Close)
	now := unixNow()
	h.Clock = func() float64 { return now }
	h.Store.Clock = h.Clock
	w.Store.store.Clock = h.Clock
	session, key, err := w.CreateBrowserSession(t.Context(), "alice", "app", "1", []string{"ui.navigate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.UpdatePageObservation("alice", session.ID, key, 1, 0, JSON{"page": "home"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	run, err := w.CreateBrowserRun(ctx, "alice", "app", session.ID, "Open the orders page, then report the confirmed page. Use the completed action receipt as evidence.", "live-browser-only")
	if err != nil {
		t.Fatal(err)
	}
	commandsRun := 0
	started := time.Now()
	for i := 0; i < 25; i++ {
		run, err = h.Drive(ctx, run.RunID, "alice")
		if err != nil {
			t.Fatalf("drive: %s", ErrorCode(err))
		}
		if terminal(run.Status) {
			break
		}
		commands, blocked, err := w.PollBrowser("alice", session.ID, key, 1)
		if err != nil || blocked {
			t.Fatal("browser dispatch", ErrorCode(err))
		}
		for _, command := range commands {
			ok, _, err := w.BeginBrowserCommand(ctx, "alice", command.ID, key, 1)
			if err != nil || !ok {
				t.Fatal("browser claim", ErrorCode(err))
			}
			page, _ := command.Arguments["page"].(string)
			if _, err = w.CompleteBrowserCommand("alice", command.ID, key, 1, "succeeded", JSON{"page": page}, ""); err != nil {
				t.Fatal("browser receipt", ErrorCode(err))
			}
			commandsRun++
		}
		now += 2
	}
	state, err := h.restore(run)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || commandsRun != 1 {
		for _, observation := range state.Observations {
			code := ""
			if observation.ErrorCode != nil {
				code = *observation.ErrorCode
			}
			t.Logf("observation capability=%s status=%s error_code=%s", observation.Capability, observation.Status, code)
		}
		for _, decision := range state.Decisions {
			t.Logf("decision kind=%v name=%v fact_id=%v path=%v", decision["kind"], decision["name"], decision["fact_id"], decision["path"])
		}
		code := ""
		if state.ErrorCode != nil {
			code = *state.ErrorCode
		}
		t.Fatalf("status=%s error_code=%s browser_commands=%d decisions=%d", run.Status, code, commandsRun, len(state.Decisions))
	}
	t.Logf("browser-only completed: decisions=%d browser_commands=%d elapsed=%s default_model_round_limit=%d", len(state.Decisions), commandsRun, time.Since(started), h.Settings.MaxModelRounds)
}
