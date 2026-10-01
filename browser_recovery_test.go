package agenstra

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserResumeDetectsChangedProfileBeforeMutatingSession(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	p := frontendTestProfile(false)
	p.Version = "2"
	compiled, err := compileFrontend(p)
	if err != nil {
		t.Fatal(err)
	}
	f.w.profiles["records-web"] = compiled
	if _, err = f.w.ResumeBrowserSession("alice", f.session.ID, f.key, 1); ErrorCode(err) != "browser_profile_changed" {
		t.Fatalf("expected profile mismatch at resume, got %v", err)
	}
	var saved BrowserSession
	if err = webLoad(f.w.Store.store.DB, "web_sessions", f.session.ID, "alice", &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Generation != 1 || saved.ProfileDigest != f.session.ProfileDigest {
		t.Fatal("failed resume changed the saved session", saved)
	}
}

func TestBrowserRecoveryPreservesUnknownHistoryAndRequiresStoppedRun(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	r := f.run(t)
	c := f.dispatch(t)
	if ok, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	recover := func(request string, acknowledge bool) (BrowserSession, string, error) {
		return f.w.RecoverBrowserSession(t.Context(), "alice", f.session.ID, f.key, 1, "1", []string{"ui.navigate"}, request, acknowledge)
	}
	if _, _, err := recover("busy", true); ErrorCode(err) != "browser_recovery_busy" {
		t.Fatal(err)
	}
	if _, err := f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "unknown", nil, "browser_handler_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recover("no-confirmation", false); ErrorCode(err) != "browser_outcome_unresolved" {
		t.Fatal(err)
	}
	if _, _, err := recover("still-active", true); ErrorCode(err) != "browser_recovery_run_active" {
		t.Fatal(err)
	}
	if _, err := f.h.Cancel(t.Context(), r.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	if stopped, err := f.h.Drive(t.Context(), r.RunID, "alice"); err != nil || stopped.Status != "cancelled" {
		t.Fatal(stopped.Status, err)
	}
	s, key, err := recover("verified", true)
	if err != nil || s.ID == f.session.ID || key == f.key || s.KeyHash != "" {
		t.Fatal(s, key, err)
	}
	if _, _, err = f.w.PollBrowser("alice", f.session.ID, f.key, 1); ErrorCode(err) != "browser_generation_changed" {
		t.Fatal(err)
	}
	commands, blocked, err := f.w.PollBrowser("alice", s.ID, key, 1)
	if err != nil || blocked || len(commands) != 0 {
		t.Fatal(commands, blocked, err)
	}
	old, err := f.w.command("alice", c.ID)
	if err != nil || old.Status != "unknown" || old.ErrorCode != "browser_handler_outcome_unknown" {
		t.Fatal(old, err)
	}
	// Lost recovery responses return the same key/session, never another tab.
	retry, retryKey, err := recover("verified", true)
	if err != nil || retry.ID != s.ID || retryKey != key {
		t.Fatal(retry, err)
	}
	if _, _, err = recover("verified", false); ErrorCode(err) != "browser_recovery_conflict" {
		t.Fatal("changed recovery arguments accepted", err)
	}
	var count int
	if err = f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_sessions").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	// Even after replacement a late original result can settle its own evidence.
	if _, err = f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "succeeded", JSON{"page": "orders"}, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, key, 1); err == nil || ok {
		t.Fatal("old command could execute", ok, err)
	}
}

func TestBrowserRecoveryChecksQueuedTasksInOtherConversations(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	c, err := f.w.CreateConversation(t.Context(), "alice", "records-web")
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, "queued", "Open orders", f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.w.RecoverBrowserSession(t.Context(), "alice", f.session.ID, f.key, 1, "1", []string{"ui.navigate"}, "queued-check", true); ErrorCode(err) != "browser_recovery_run_active" {
		t.Fatal(err)
	}
	if _, err = f.w.CancelMessage(t.Context(), "alice", m.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.w.RecoverBrowserSession(t.Context(), "alice", f.session.ID, f.key, 1, "1", []string{"ui.navigate"}, "after-stop", false); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserRecoveryValidatesOwnerGenerationAndNewRegistration(t *testing.T) {
	for _, tc := range []struct {
		name, owner, key, version, request, want string
		generation                               int
		handlers                                 []string
	}{
		{"wrong owner", "bob", "original", "1", "r", "not_found", 1, []string{"ui.navigate"}},
		{"wrong key", "alice", "forged", "1", "r", "browser_session_invalid", 1, []string{"ui.navigate"}},
		{"wrong generation", "alice", "original", "1", "r", "browser_generation_changed", 2, []string{"ui.navigate"}},
		{"wrong version", "alice", "original", "2", "r", "browser_handler_version_mismatch", 1, []string{"ui.navigate"}},
		{"unknown handler", "alice", "original", "1", "r", "browser_handler_unknown", 1, []string{"ui.forged"}},
		{"missing id", "alice", "original", "1", "", "request_id_required", 1, []string{"ui.navigate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWebFixture(t, &hostModel{}, false)
			key := tc.key
			if key == "original" {
				key = f.key
			}
			if _, _, err := f.w.RecoverBrowserSession(t.Context(), tc.owner, f.session.ID, key, tc.generation, tc.version, tc.handlers, tc.request, false); ErrorCode(err) != tc.want {
				t.Fatal(err)
			}
			var saved BrowserSession
			if err := webLoad(f.w.Store.store.DB, "web_sessions", f.session.ID, "alice", &saved); err != nil || saved.Closed {
				t.Fatal(saved, err)
			}
		})
	}
}

func TestBrowserRecoveryAdoptsCurrentProfileAndFencesOldSession(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	p := frontendTestProfile(false)
	p.Version = "2"
	p.HandlerVersion = "2"
	compiled, err := compileFrontend(p)
	if err != nil {
		t.Fatal(err)
	}
	f.w.profiles["records-web"] = compiled
	s, key, err := f.w.RecoverBrowserSession(t.Context(), "alice", f.session.ID, f.key, 1, "2", []string{"ui.navigate"}, "upgrade", false)
	if err != nil || s.ProfileDigest != compiled.digest || s.HandlerVersion != "2" || s.ContextRevision != 0 || len(s.Context) != 0 {
		t.Fatal(s, err)
	}
	if _, err = f.w.ResumeBrowserSession("alice", s.ID, key, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.w.RecoverBrowserSession(t.Context(), "alice", f.session.ID, f.key, 1, "2", []string{"ui.navigate"}, "upgrade", false); err != nil {
		t.Fatal("idempotent response after replacement resume", err)
	}
}

func TestBrowserRecoveryConcurrentRetriesReturnOneReplacement(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	type result struct {
		session BrowserSession
		key     string
		err     error
	}
	const attempts = 8
	results := make(chan result, attempts)
	start := make(chan struct{})
	for range attempts {
		go func() {
			<-start
			s, key, err := f.w.RecoverBrowserSession(t.Context(), "alice", f.session.ID, f.key, 1, "1", []string{"ui.navigate"}, "concurrent-retry", false)
			results <- result{s, key, err}
		}()
	}
	close(start)
	var first result
	for i := range attempts {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if i == 0 {
			first = r
		} else if r.session.ID != first.session.ID || r.key != first.key {
			t.Fatal("concurrent retries returned different replacements")
		}
	}
	var count int
	if err := f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_sessions").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

func TestBrowserRecoveryHTTPRequiresTicketAndBrowserKey(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	s := &HTTPServer{Host: f.h, Web: f.w}
	ticket, err := f.w.MintSession("alice")
	if err != nil {
		t.Fatal(err)
	}
	path := "/browser/v1/sessions/" + f.session.ID + "/recover"
	body := `{"generation":1,"handler_version":"1","handlers":["ui.navigate"],"request_id":"http-recovery","acknowledge_unknown":false}`
	for _, tc := range []struct {
		token, key, body string
		want             int
	}{
		{"", f.key, body, 401}, {ticket, "forged", body, 401}, {ticket, f.key, "{}", 422}, {ticket, f.key, body, 200},
	} {
		r := httptest.NewRequest("POST", path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("X-Agenstra-Browser-Key", tc.key)
		out := httptest.NewRecorder()
		s.webHTTP(out, r)
		if out.Code != tc.want {
			t.Fatal(out.Code, out.Body.String())
		}
		if out.Code == 200 {
			var result struct {
				Session BrowserSession
				Key     string
			}
			if err = json.Unmarshal(out.Body.Bytes(), &result); err != nil || result.Key == "" || result.Session.KeyHash != "" {
				t.Fatal(result, err)
			}
		}
	}
}
