package agenstra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type webFixture struct {
	w       *WebIntegration
	h       *AgentHost
	d       *Deployment
	p       *hostProvider
	now     float64
	session BrowserSession
	key     string
}

func frontendTestProfile(approval bool) FrontendProfile {
	return FrontendProfile{Schema: "agenstra.frontend-profile.v1", Version: "1", HandlerVersion: "1", ContextSchema: JSON{"type": "object"}, Actions: []FrontendAction{{Name: "ui.navigate", Description: "Open a page", InputSchema: JSON{"type": "object", "properties": JSON{"page": JSON{"type": "string"}}, "required": []any{"page"}, "additionalProperties": false}, OutputSchema: JSON{"type": "object", "properties": JSON{"page": JSON{"type": "string"}}, "required": []any{"page"}, "additionalProperties": false}, Effect: "write", ApprovalRequired: approval, TimeoutSeconds: 5}}}
}
func newWebFixture(t *testing.T, model *hostModel, approval bool) *webFixture {
	t.Helper()
	dir := t.TempDir()
	raw, callErr := json.Marshal(frontendTestProfile(approval))
	if callErr != nil {
		t.Error(callErr)
	}
	if e := os.WriteFile(filepath.Join(dir, "frontend.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	p := &hostProvider{}
	h := testHost(t, testStore(t), p, model)
	cfg := WebIntegrationConfig{DatabasePath: "web.sqlite3", Chat: true, BrowserBridge: true, SessionKeyEnv: "WEB_KEY", Integrations: map[string]WebProfileConfig{"records": {PackID: "records"}, "records-web": {PackID: "records", FrontendProfilePath: "frontend.json"}}}
	d := &Deployment{BaseDir: dir, Environment: map[string]string{"WEB_KEY": strings.Repeat("k", 32), "ALICE_KEY": "alice-key"}, Config: DeploymentConfig{DatabasePath: h.Store.Path, Packs: map[string]PackConfig{"records": {Path: "unused"}}, Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY", BrowserActions: map[string][]string{"records-web": {"ui.navigate"}}}}, WebIntegration: &cfg}}
	w, e := NewWebIntegration(h, d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f := &webFixture{w: w, h: h, d: d, p: p, now: float64(time.Now().Unix())}
	h.Clock = func() float64 { return f.now }
	h.Store.Clock = h.Clock
	w.Store.store.Clock = h.Clock
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	f.session, f.key, e = w.CreateBrowserSession(t.Context(), "alice", "records-web", "1", []string{"ui.navigate"})
	if e != nil {
		t.Fatal(e)
	}
	f.session, e = w.UpdatePageObservation("alice", f.session.ID, f.key, 1, 0, JSON{"page": "home"})
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func browserDecisions() []Decision {
	return []Decision{
		{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "context-1", Capability: "ui.get_context", Arguments: JSON{}, Reason: "Read current page"}}},
		{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "navigate-1", Capability: "ui.navigate", Arguments: JSON{"page": "orders"}, Reason: "Open orders"}}},
	}
}
func (f *webFixture) run(t *testing.T) StoredRun {
	t.Helper()
	r, e := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Open orders", "request-1")
	if e != nil {
		t.Fatal(e)
	}
	r, e = f.h.Drive(t.Context(), r.RunID, "alice")
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func (f *webFixture) dispatch(t *testing.T) BrowserCommand {
	t.Helper()
	commands, blocked, e := f.w.PollBrowser("alice", f.session.ID, f.key, f.session.Generation)
	if e != nil || blocked || len(commands) != 1 {
		t.Fatalf("poll: %v %v %v", commands, blocked, e)
	}
	return commands[0]
}
func TestBrowserCommandExecutionAndDeduplication(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	r := f.run(t)
	if r.Status != "waiting" {
		t.Fatalf("status: %s", r.Status)
	}
	c := f.dispatch(t)
	accepted, _, e := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1)
	if e != nil || !accepted {
		t.Fatalf("begin: %v %v", accepted, e)
	}
	accepted, _, e = f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1)
	if e != nil || accepted {
		t.Fatalf("duplicate claim: %v %v", accepted, e)
	}
	result := JSON{"page": "orders"}
	if _, e = f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "succeeded", result, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "succeeded", result, ""); e != nil {
		t.Fatal("duplicate ACK", e)
	}
	if _, e = f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "succeeded", JSON{"page": "other"}, ""); ErrorCode(e) != "browser_result_conflict" {
		t.Fatal(e)
	}
	f.now += 2
	r, e = f.h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "completed" {
		t.Fatalf("complete: %s %v", r.Status, e)
	}
	var count int
	if err := f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_commands").Scan(&count); err != nil {
		t.Error(err)
	}
	if count != 1 {
		t.Fatal("duplicate command", count)
	}
}
func TestBrowserLostAckReconciliationAndGenerationFence(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	r := f.run(t)
	c := f.dispatch(t)
	if ok, _, e := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1); e != nil || !ok {
		t.Fatal(ok, e)
	}
	// Handler has run, but the ACK is lost. Restarting a tab cannot claim it again.
	s, e := f.w.ResumeBrowserSession("alice", f.session.ID, f.key, 1)
	if e != nil || s.Generation != 2 {
		t.Fatal(s, e)
	}
	if _, _, e = f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 2); ErrorCode(e) != "browser_generation_changed" {
		t.Fatal(e)
	}
	f.now += 2
	r, e = f.h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "needs_reconciliation" {
		t.Fatalf("unknown outcome: %s %v", r.Status, e)
	}
	if _, e = f.w.ReconcileBrowserCommand(t.Context(), "alice", c.ID, r.Revision); ErrorCode(e) != "browser_result_not_verified" {
		t.Fatal(e)
	}
	if _, e = f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "succeeded", JSON{"page": "orders"}, ""); e != nil {
		t.Fatal("late original ACK", e)
	}
	if _, e = f.w.ReconcileBrowserCommand(t.Context(), "alice", c.ID, r.Revision+1); ErrorCode(e) != "revision_conflict" {
		t.Fatal(e)
	}
	r, e = f.w.ReconcileBrowserCommand(t.Context(), "alice", c.ID, r.Revision)
	if e != nil {
		t.Fatal(e)
	}
	r, e = f.h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "completed" {
		t.Fatalf("reconciled: %s %v", r.Status, e)
	}
	var count int
	if err := f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_commands").Scan(&count); err != nil {
		t.Error(err)
	}
	if count != 1 {
		t.Fatal("reconciliation replayed handler")
	}
}
func TestBrowserTimeoutAndContextChanges(t *testing.T) {
	t.Run("executed but no ACK", func(t *testing.T) {
		f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
		r := f.run(t)
		c := f.dispatch(t)
		if ok, _, e := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1); e != nil || !ok {
			t.Fatal(ok, e)
		}
		f.now += 20
		r, e := f.h.Drive(t.Context(), r.RunID, "alice")
		if e != nil || r.Status != "needs_reconciliation" {
			t.Fatal(r.Status, e)
		}
	})
	t.Run("stale page rejected before execution", func(t *testing.T) {
		f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
		f.run(t)
		c := f.dispatch(t)
		if _, e := f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, JSON{"page": "changed"}); e != nil {
			t.Fatal(e)
		}
		ok, c, e := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1)
		if e != nil || ok || c.Status != "failed" || c.ErrorCode != "browser_context_changed" {
			t.Fatal(ok, c, e)
		}
	})
	t.Run("never started expires without reconciliation", func(t *testing.T) {
		f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
		r := f.run(t)
		c := f.dispatch(t)
		f.now += 6
		c, e := f.w.command("alice", c.ID)
		if e != nil || c.Status != "expired" {
			t.Fatal(c, e)
		}
		r, e = f.h.Drive(t.Context(), r.RunID, "alice")
		if e != nil || r.Status != "completed" {
			t.Fatal(r.Status, e)
		}
	})
}
func TestBrowserApprovalAndLiveGrantRevocation(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, true)
	r := f.run(t)
	if r.Status != "needs_approval" {
		t.Fatal(r.Status)
	}
	state, e := f.h.restore(r)
	if e != nil {
		t.Fatal(e)
	}
	item := state.Pending[0]
	if _, e = f.h.Approve(t.Context(), r.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, r.Revision, true); e != nil {
		t.Fatal(e)
	}
	r, e = f.h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "waiting" {
		t.Fatal(r.Status, e)
	}
	c := f.dispatch(t)
	f.w.ResolveBrowserActions = func(context.Context, string, string) ([]string, error) { return nil, nil }
	if ok, _, e := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1); ok || ErrorCode(e) != "capability_not_granted" {
		t.Fatal(ok, e)
	}
	f.w.ResolveBrowserActions = nil
	f.now += 1000
	if ok, _, e := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1); ok || ErrorCode(e) != "browser_approval_expired" {
		t.Fatal(ok, e)
	}
}
func TestChatQueuePrebindingIdempotenceAndRecovery(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	c, e := f.w.CreateConversation(t.Context(), "alice", "records-web")
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := f.w.SubmitMessage(t.Context(), "alice", c.ID, "first", "First task", f.session.ID); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	msgs, e := f.w.Store.messages(f.w.Store.store.DB, "alice", c.ID)
	if e != nil || len(msgs) != 1 {
		t.Fatal(msgs, e)
	}
	if _, e = f.w.Store.binding("alice", msgs[0].RunID); e != nil {
		t.Fatal("binding not durable before Create", e)
	}
	if _, e = f.h.Get(t.Context(), msgs[0].RunID, "alice"); !errors.Is(e, ErrRunNotFound) {
		t.Fatal("run started before activation", e)
	}
	if _, e = f.w.SubmitMessage(t.Context(), "alice", c.ID, "first", "different task", f.session.ID); ErrorCode(e) != "chat_message_conflict" {
		t.Fatal(e)
	}
	second, e := f.w.SubmitMessage(t.Context(), "alice", c.ID, "second", "Follow up", f.session.ID)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := f.w.Tick(t.Context()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if _, e = f.h.Get(t.Context(), second.RunID, "alice"); !errors.Is(e, ErrRunNotFound) {
		t.Fatal("parallel conversation run", e)
	}
	if _, e = f.h.Drive(t.Context(), msgs[0].RunID, "alice"); e != nil {
		t.Fatal(e)
	}
	if e = f.w.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	r, e := f.h.Get(t.Context(), second.RunID, "alice")
	if e != nil {
		t.Fatal(e)
	}
	state, e := f.h.restore(r)
	if e != nil || !strings.Contains(state.Instruction, "First task") || !strings.Contains(state.Instruction, "Done") {
		t.Fatal(state, e)
	}
	if _, _, e = f.w.Conversation(t.Context(), "bob", c.ID); ErrorCode(e) != "not_found" {
		t.Fatal("cross owner", e)
	}
	var v int
	if err := f.h.Store.DB.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Error(err)
	}
	if v != 1 {
		t.Fatal("changed run schema", v)
	}
	// Simulate response loss after Create: recovery must reuse this run.
	if e = f.w.Store.store.write(func(tx *sql.Tx) error {
		m := second
		m.Status = "creating"
		m.Instruction = state.Instruction
		return webSave(tx, "web_messages", m.ID, m)
	}); e != nil {
		t.Fatal(e)
	}
	if e = f.w.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	var count int
	if err := f.h.Store.DB.QueryRow("SELECT count(*) FROM runs").Scan(&count); err != nil {
		t.Error(err)
	}
	if count != 2 {
		t.Fatal("recovery duplicated run", count)
	}
}
func TestWebReleasePinningAndHeadlessFingerprint(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	original := fingerprint(f.p)
	p, e := f.h.ProviderFactory(t.Context(), "alice", "records")
	if e != nil || fingerprint(p) != original {
		t.Fatal("headless fingerprint changed", e)
	}
	r, e := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Open orders", "request-1")
	if e != nil {
		t.Fatal(e)
	}
	release := r.State["pack_release"].(string)
	updated := frontendTestProfile(false)
	updated.Version = "2"
	updated.HandlerVersion = "2"
	raw, callErr2 := json.Marshal(updated)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if err := os.WriteFile(filepath.Join(f.d.BaseDir, "frontend.json"), raw, 0600); err != nil {
		t.Error(err)
	}
	// A separate host simulates a process restart with a new configured profile.
	if err := f.w.Close(); err != nil {
		t.Error(err)
	}
	h := testHost(t, f.h.Store, f.p, &hostModel{decisions: browserDecisions()})
	w, e := NewWebIntegration(h, f.d, *f.d.Config.WebIntegration)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(w.Close)
	old, e := h.ReleaseProviderFactory(t.Context(), "alice", "records-web", release)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(old.Close)
	if old.Capabilities()["ui.navigate"].Version != "1" {
		t.Fatal("old run uses latest frontend contract")
	}
	current, e := h.ProviderFactory(t.Context(), "alice", "records-web")
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(current.Close)
	if current.Capabilities()["ui.navigate"].Version != "2" {
		t.Fatal("new run did not use new profile")
	}
	if fingerprint(old) == fingerprint(current) {
		t.Fatal("profile not included in fingerprint")
	}
}
func TestWebHTTPAuthenticationAndOptionalRoutes(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	// NewHTTPServer attaches the extension itself, so use a fresh unwrapped host.
	if err := f.w.Close(); err != nil {
		t.Error(err)
	}
	h := testHost(t, f.h.Store, f.p, &hostModel{})
	s, e := NewHTTPServer(h, f.d, false, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(s.Close)
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, r)
		return out
	}
	minted := request("POST", "/web/v1/token", "alice-key", "")
	if minted.Code != 200 {
		t.Fatal(minted.Code, minted.Body.String())
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(minted.Body.Bytes(), &data); err != nil {
		t.Error(err)
	}
	if out := request("GET", "/runs", data.Token, ""); out.Code != 401 {
		t.Fatal("web ticket accessed headless API", out.Code)
	}
	if out := request("GET", "/admin/api/overview", data.Token, ""); out.Code == 200 {
		t.Fatal("web ticket accessed admin")
	}
	if out := request("POST", "/chat/v1/conversations", data.Token, `{"integration_id":"records"}`); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	forged := data.Token[:len(data.Token)-3] + "abc"
	if out := request("GET", "/chat/v1/conversations", forged, ""); out.Code != 401 {
		t.Fatal(out.Code)
	}
	if out := request("GET", "/web/assets/agenstra-client.js", "", ""); out.Code != 200 || !strings.Contains(out.Body.String(), "createAgenstraClient") {
		t.Fatal(out.Code)
	}
	legacy, e := h.Create(t.Context(), "alice", "records", "Legacy task", "legacy")
	if e != nil {
		t.Fatal(e)
	}
	if out := request("GET", "/web/v1/runs/"+legacy.RunID, data.Token, ""); out.Code != 404 {
		t.Fatal("web ticket accessed unrelated run", out.Code)
	}
	r := httptest.NewRequest("POST", "/chat/v1/conversations", strings.NewReader(`{"integration_id":"records"}`))
	r.Header.Set("Origin", "https://other.example")
	r.Header.Set("Authorization", "Bearer "+data.Token)
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, r)
	if out.Code != 403 {
		t.Fatal(out.Code)
	}
}

func TestChatConcurrentCreateFailureKeepsActiveSlot(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	c, callErr3 := f.w.CreateConversation(t.Context(), "alice", "records")
	if callErr3 != nil {
		t.Error(callErr3)
	}
	first, callErr4 := f.w.SubmitMessage(t.Context(), "alice", c.ID, "first", "One", "")
	if callErr4 != nil {
		t.Error(callErr4)
	}
	second, callErr5 := f.w.SubmitMessage(t.Context(), "alice", c.ID, "second", "Two", "")
	if callErr5 != nil {
		t.Error(callErr5)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.w.baseRelease = func(context.Context, string, string) (string, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return "", nil
		}
		return "", hostError("authorization_unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- f.w.advanceConversation(t.Context(), "alice", c.ID) }()
	<-entered
	if e := f.w.advanceConversation(t.Context(), "alice", c.ID); ErrorCode(e) != "authorization_unavailable" {
		t.Fatal(e)
	}
	var pending ChatMessage
	if e := webLoad(f.w.Store.store.DB, "web_messages", first.ID, "alice", &pending); e != nil || pending.Status != "creating" {
		t.Fatal(pending, e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if _, e := f.h.Store.GetRun(second.RunID, "alice"); !errors.Is(e, ErrRunNotFound) {
		t.Fatal("second run published before first completed", e)
	}
}

func TestChatCancellationReportsUnpublishedRunStorageFailure(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	c, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, "first", "Do not execute", "")
	if err != nil {
		t.Fatal(err)
	}
	// A creator has reserved the message but has not published a run yet.
	m.Status, m.Instruction = "creating", "Do not execute"
	if err = f.w.Store.store.write(func(tx *sql.Tx) error {
		return webSave(tx, "web_messages", m.ID, m)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.Store.DB.Exec(`CREATE TRIGGER reject_unpublished_cancel BEFORE INSERT ON runs BEGIN SELECT RAISE(FAIL, 'cannot persist cancellation'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.w.CancelMessage(t.Context(), "alice", m.ID); err == nil {
		t.Fatal("cancellation must report failure to persist the unpublished run fence")
	}
	if _, err = f.h.Store.GetRun(m.RunID, "alice"); !errors.Is(err, ErrRunNotFound) {
		t.Fatal("failed cancellation created a run", err)
	}
}

func TestChatCancellationFencesConcurrentUnpublishedCreate(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	c, callErr6 := f.w.CreateConversation(t.Context(), "alice", "records")
	if callErr6 != nil {
		t.Error(callErr6)
	}
	m, callErr7 := f.w.SubmitMessage(t.Context(), "alice", c.ID, "first", "Do not execute", "")
	if callErr7 != nil {
		t.Error(callErr7)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	f.w.baseRelease = func(context.Context, string, string) (string, error) { close(entered); <-release; return "", nil }
	done := make(chan error, 1)
	go func() { done <- f.w.advanceConversation(t.Context(), "alice", c.ID) }()
	<-entered
	if _, e := f.w.CancelMessage(t.Context(), "alice", m.ID); e != nil {
		t.Fatal(e)
	}
	run, e := f.h.Store.GetRun(m.RunID, "alice")
	if e != nil || run.Status != "cancelled" || !run.CancelRequested {
		t.Fatal("cancellation exposed queued run", run, e)
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	run, e = f.h.Drive(t.Context(), m.RunID, "alice")
	if e != nil || run.Status != "cancelled" {
		t.Fatal(run, e)
	}
	if f.p.calls != 0 {
		t.Fatal("cancelled creation invoked backend", f.p.calls)
	}
}

func TestWebStaticBaseContractPinnedBeforeFirstDrive(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	run, e := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Open orders", "pin")
	if e != nil {
		t.Fatal(e)
	}
	f.p.caps = map[string]CapabilityDescription{"records.get": {Name: "records.get", Version: "2", Description: "Changed", InputSchema: JSON{"type": "object"}, Effect: "read", Replay: "safe"}}
	_, e = f.h.ReleaseProviderFactory(t.Context(), "alice", "records-web", run.State["pack_release"].(string))
	if ErrorCode(e) != "web_base_contract_changed" {
		t.Fatal("changed backend accepted for old release", e)
	}
	if f.p.calls != 0 {
		t.Fatal("invoked changed backend")
	}
}

func TestBrowserResultSchemaAndIsolation(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	f.run(t)
	c := f.dispatch(t)
	if _, _, e := f.w.BeginBrowserCommand(t.Context(), "bob", c.ID, f.key, 1); ErrorCode(e) != "not_found" {
		t.Fatal(e)
	}
	if _, _, e := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, "wrong-key", 1); ErrorCode(e) != "browser_session_invalid" {
		t.Fatal(e)
	}
	if _, _, e := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1); e != nil {
		t.Fatal(e)
	}
	if _, e := f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "succeeded", JSON{"page": 42}, ""); ErrorCode(e) != "browser_result_invalid" {
		t.Fatal(e)
	}
	p, e := f.h.ProviderFactory(t.Context(), "alice", "records-web")
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(p.Close)
	resultSchema := p.Capabilities()["ui.navigate"].OutputSchema["properties"].(JSON)["result"]
	if webHash(resultSchema) != webHash(frontendTestProfile(false).Actions[0].OutputSchema) {
		t.Fatal("action result schema hidden")
	}
}

func TestWebDisabledKeepsLegacyRoutes(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	if err := f.w.Close(); err != nil {
		t.Error(err)
	}
	f.d.Config.WebIntegration = nil
	h := testHost(t, f.h.Store, f.p, &hostModel{})
	s, e := NewHTTPServer(h, f.d, false, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(s.Close)
	if s.Web != nil {
		t.Fatal("extension enabled by default")
	}
	for _, path := range []string{"/web/assets/agenstra-client.js", "/chat/v1/conversations", "/browser/v1/sessions"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer alice-key")
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, r)
		if out.Code != 404 {
			t.Fatal(path, out.Code)
		}
	}
}

// Multiple sequential page changes need fresh observations in the same run.
func TestBrowserRepeatedPageObservationAcrossThreeActions(t *testing.T) {
	decisions := []Decision{}
	for i := 1; i <= 3; i++ {
		decisions = append(decisions,
			Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: fmt.Sprintf("context-%d", i), Capability: "ui.get_context", Arguments: JSON{}, Reason: "Refresh page revision"}}},
			Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: fmt.Sprintf("navigate-%d", i), Capability: "ui.navigate", Arguments: JSON{"page": fmt.Sprintf("page-%d", i)}, Reason: "Next page"}}})
	}
	f := newWebFixture(t, &hostModel{decisions: decisions}, false)
	r := f.run(t)
	for i := 1; i <= 3; i++ {
		if r.Status != "waiting" {
			t.Fatalf("step %d: %s", i, r.Status)
		}
		c := f.dispatch(t)
		accepted, _, e := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1)
		if e != nil || !accepted {
			t.Fatalf("step %d begin: %v %v", i, accepted, e)
		}
		page := JSON{"page": fmt.Sprintf("page-%d", i)}
		f.session, e = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, page)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "succeeded", page, ""); e != nil {
			t.Fatal(e)
		}
		f.now += 2
		r, e = f.h.Drive(t.Context(), r.RunID, "alice")
		if e != nil {
			t.Fatal(e)
		}
	}
	if r.Status != "completed" {
		t.Fatalf("final: %s", r.Status)
	}
}

func TestBrowserHostReadCanRefreshAfterPageChanges(t *testing.T) {
	decisions := []Decision{}
	for i := 1; i <= 3; i++ {
		decisions = append(decisions,
			Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: fmt.Sprintf("context-%d", i), Capability: "ui.get_context", Arguments: JSON{}, Reason: "Read latest page revision"}}},
			Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: fmt.Sprintf("read-%d", i), Capability: "ui.read_activity", Arguments: JSON{}, Reason: "Read business state again after page changes"}}})
	}
	f := newWebFixture(t, &hostModel{decisions: decisions}, false)
	profile := frontendTestProfile(false)
	profile.Actions[0].Name, profile.Actions[0].Effect = "ui.read_activity", "read"
	profile.Actions[0].InputSchema = JSON{"type": "object", "additionalProperties": false}
	compiled, err := compileFrontend(profile)
	if err != nil {
		t.Fatal(err)
	}
	f.w.profiles["records-web"] = compiled
	user := f.d.Config.Users["alice"]
	user.BrowserActions["records-web"] = []string{"ui.read_activity"}
	f.d.Config.Users["alice"] = user
	f.session, f.key, err = f.w.CreateBrowserSession(t.Context(), "alice", "records-web", "1", []string{"ui.read_activity"})
	if err != nil {
		t.Fatal(err)
	}
	f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, 0, JSON{"page": "before"})
	if err != nil {
		t.Fatal(err)
	}
	run := f.run(t)
	for i := 1; i <= 3; i++ {
		if run.Status != "waiting" {
			t.Fatalf("read %d was not dispatched: %s", i, run.Status)
		}
		command := f.dispatch(t)
		accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1)
		if err != nil || !accepted {
			t.Fatal(accepted, err)
		}
		page := JSON{"page": fmt.Sprintf("revision-%d", i)}
		f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, page)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "succeeded", page, ""); err != nil {
			t.Fatal(err)
		}
		f.now += 2
		run, err = f.h.Drive(t.Context(), run.RunID, "alice")
		if err != nil {
			t.Fatal(err)
		}
	}
	if run.Status != "completed" {
		t.Fatal(run.Status)
	}
}

func TestChatTerminalFailureRetainsRunErrorCode(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	f.h.Settings.MaxModelRounds = 1
	c, e := f.w.CreateConversation(t.Context(), "alice", "records-web")
	if e != nil {
		t.Fatal(e)
	}
	m, e := f.w.SubmitMessage(t.Context(), "alice", c.ID, "budget-failure", "Open orders", f.session.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.w.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	run, e := f.h.Drive(t.Context(), m.RunID, "alice")
	if e != nil || run.Status != "failed" {
		t.Fatalf("run: %s %v", run.Status, e)
	}
	_, messages, e := f.w.Conversation(t.Context(), "alice", c.ID)
	if e != nil || len(messages) != 1 {
		t.Fatal(messages, e)
	}
	if messages[0].Status != "failed" || messages[0].ErrorCode != "model_round_budget_exhausted" {
		t.Fatalf("terminal message lost failure reason: %+v", messages[0])
	}
}
