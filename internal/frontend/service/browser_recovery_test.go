package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
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
	if _, err = f.w.ResumeBrowserSession("alice", f.session.ID, f.key, 1); agentcontract.ErrorCode(err) != "browser_profile_changed" {
		t.Fatalf("expected profile mismatch at resume, got %v", err)
	}
	var saved agentcontract.BrowserSession
	if err = webLoad(f.w.Store.store.DB, "web_sessions", f.session.ID, "alice", &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Generation != 1 || saved.ProfileDigest != f.session.ProfileDigest {
		t.Fatal("failed resume changed the saved session", saved)
	}
}

func TestNewBrowserWorkReportsProfileChangeWithoutFencingExistingWork(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	f.run(t)
	p := frontendTestProfile(false)
	p.Version = "2"
	compiled, err := compileFrontend(p)
	if err != nil {
		t.Fatal(err)
	}
	f.w.profiles["records-web"] = compiled
	if _, err = f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "New work", "new-request"); agentcontract.ErrorCode(err) != "browser_profile_changed" {
		t.Fatalf("new work did not identify its stale contract: %v", err)
	}
	conversation, err := f.w.CreateConversation(t.Context(), "alice", "records-web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.w.SubmitMessage(t.Context(), "alice", conversation.ID, "new-message", "New work", f.session.ID); agentcontract.ErrorCode(err) != "browser_profile_changed" {
		t.Fatalf("new message did not identify its stale contract: %v", err)
	}
	messages, err := f.w.Store.messages(f.w.Store.store.DB, "alice", conversation.ID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("rejected message was published: %v %v", messages, err)
	}
	command := f.dispatch(t)
	if command.Generation != f.session.Generation || command.Status != "dispatched" {
		t.Fatal("profile update fenced existing work", command)
	}
}

func TestBrowserResumeRetryPreservesCurrentPageAndRunningCommand(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	first, err := f.w.resumeBrowserSession("alice", f.session.ID, f.key, 1, "refresh-1")
	if err != nil || first.Generation != 2 || first.ResumeRequestID != "" {
		t.Fatal(first, err)
	}
	f.session, err = f.w.UpdatePageObservation("alice", first.ID, f.key, 2, first.ContextRevision, agentcontract.JSON{"page": "orders"})
	if err != nil || f.session.ResumeRequestID != "" {
		t.Fatal(f.session, err)
	}
	f.run(t)
	command := f.dispatch(t)
	if ok, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 2); err != nil || !ok {
		t.Fatal(ok, err)
	}
	retry, err := f.w.resumeBrowserSession("alice", first.ID, f.key, 1, "refresh-1")
	if err != nil || retry.Generation != 2 || retry.ContextRevision != f.session.ContextRevision || retry.Context["page"] != "orders" {
		t.Fatal(retry, err)
	}
	current, err := f.w.command("alice", command.ID)
	if err != nil || current.Status != "running" || current.Generation != 2 {
		t.Fatal(current, err)
	}
	for _, tc := range []struct {
		owner, key, request string
		generation          int
		want                string
	}{
		{"alice", f.key, "different-refresh", 1, "browser_generation_changed"},
		{"alice", f.key, "refresh-1", 2, "request_id_conflict"},
		{"alice", "wrong-key", "refresh-1", 1, "browser_session_invalid"},
		{"other", f.key, "refresh-1", 1, "not_found"},
		{"alice", f.key, strings.Repeat("x", 129), 2, "request_id_required"},
	} {
		if _, err := f.w.resumeBrowserSession(tc.owner, first.ID, tc.key, tc.generation, tc.request); agentcontract.ErrorCode(err) != tc.want {
			t.Fatal(tc, err)
		}
	}

	// A later legacy transition still fences the previous request and command.
	latest, err := f.w.ResumeBrowserSession("alice", first.ID, f.key, 2)
	if err != nil || latest.Generation != 3 {
		t.Fatal(latest, err)
	}
	if _, err = f.w.resumeBrowserSession("alice", first.ID, f.key, 1, "refresh-1"); agentcontract.ErrorCode(err) != "browser_generation_changed" {
		t.Fatal(err)
	}
	current, err = f.w.command("alice", command.ID)
	if err != nil || current.Status != "unknown" {
		t.Fatal(current, err)
	}
}

func TestBrowserResumeConcurrentRetriesAdvanceOnce(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			s, err := f.w.resumeBrowserSession("alice", f.session.ID, f.key, 1, "same-refresh")
			if err == nil && s.Generation != 2 {
				err = agentcontract.NewHostError("unexpected_generation")
			}
			results <- err
		}()
	}
	close(start)
	var firstErr error
	for range 8 {
		if err := <-results; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		t.Fatal(firstErr)
	}
}

func TestBrowserResumeRetryCannotBypassClosureOrProfileChange(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "profile_changed"}[changed], func(t *testing.T) {
			f := newWebFixture(t, &hostModel{}, false)
			if _, err := f.w.resumeBrowserSession("alice", f.session.ID, f.key, 1, "retry"); err != nil {
				t.Fatal(err)
			}
			want := "browser_generation_changed"
			if changed {
				profile := frontendTestProfile(false)
				profile.Version = "2"
				compiled, err := compileFrontend(profile)
				if err != nil {
					t.Fatal(err)
				}
				f.w.profiles["records-web"] = compiled
				want = "browser_profile_changed"
			} else if err := f.w.CloseBrowserSession("alice", f.session.ID, f.key, 2); err != nil {
				t.Fatal(err)
			}
			if _, err := f.w.resumeBrowserSession("alice", f.session.ID, f.key, 1, "retry"); agentcontract.ErrorCode(err) != want {
				t.Fatal(err)
			}
		})
	}
}

func TestBrowserResumeHTTPRetryRequiresOriginalOwnerKeyAndRequest(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	s := &HTTPServer{Host: f.h, Web: f.w}
	ticket, err := f.w.MintSession("alice")
	if err != nil {
		t.Fatal(err)
	}
	path := "/browser/v1/sessions/" + f.session.ID + "/resume"
	for _, tc := range []struct {
		token, key, body string
		want             int
	}{
		{"", f.key, `{"generation":1,"request_id":"http-resume"}`, 401},
		{ticket, "wrong-key", `{"generation":1,"request_id":"http-resume"}`, 401},
		{ticket, f.key, `{"generation":1,"request_id":"http-resume"}`, 200},
		{ticket, f.key, `{"generation":1,"request_id":"http-resume"}`, 200},
		{ticket, f.key, `{"generation":1,"request_id":"different"}`, 409},
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
			var result struct{ Session agentcontract.BrowserSession }
			if err = json.Unmarshal(out.Body.Bytes(), &result); err != nil || result.Session.Generation != 2 || result.Session.KeyHash != "" || result.Session.ResumeRequestID != "" {
				t.Fatal(result, err)
			}
		}
	}
}

func TestBrowserRecoveryPreservesUnknownHistoryAndRequiresStoppedRun(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	r := f.run(t)
	c := f.dispatch(t)
	if ok, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", c.ID, f.key, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	recover := func(request string, acknowledge bool) (agentcontract.BrowserSession, string, error) {
		return f.w.RecoverBrowserSession(t.Context(), "alice", f.session.ID, f.key, 1, "1", []string{"ui.navigate"}, request, acknowledge)
	}
	if _, _, err := recover("busy", true); agentcontract.ErrorCode(err) != "browser_recovery_busy" {
		t.Fatal(err)
	}
	if _, err := f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "unknown", nil, "browser_handler_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recover("no-confirmation", false); agentcontract.ErrorCode(err) != "browser_outcome_unresolved" {
		t.Fatal(err)
	}
	if _, _, err := recover("still-active", true); agentcontract.ErrorCode(err) != "browser_recovery_run_active" {
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
	if _, _, err = f.w.PollBrowser("alice", f.session.ID, f.key, 1); agentcontract.ErrorCode(err) != "browser_generation_changed" {
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
	if _, _, err = recover("verified", false); agentcontract.ErrorCode(err) != "browser_recovery_conflict" {
		t.Fatal("changed recovery arguments accepted", err)
	}
	var count int
	if err = f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_sessions").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}

	// Even after replacement a late original result can settle its own evidence.
	if _, err = f.w.CompleteBrowserCommand("alice", c.ID, f.key, 1, "succeeded", agentcontract.JSON{"page": "orders"}, ""); err != nil {
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
	if _, _, err = f.w.RecoverBrowserSession(t.Context(), "alice", f.session.ID, f.key, 1, "1", []string{"ui.navigate"}, "queued-check", true); agentcontract.ErrorCode(err) != "browser_recovery_run_active" {
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
			if _, _, err := f.w.RecoverBrowserSession(t.Context(), tc.owner, f.session.ID, key, tc.generation, tc.version, tc.handlers, tc.request, false); agentcontract.ErrorCode(err) != tc.want {
				t.Fatal(err)
			}
			var saved agentcontract.BrowserSession
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
		session agentcontract.BrowserSession
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
				Session agentcontract.BrowserSession
				Key     string
			}
			if err = json.Unmarshal(out.Body.Bytes(), &result); err != nil || result.Key == "" || result.Session.KeyHash != "" {
				t.Fatal(result, err)
			}
		}
	}
}

func TestBrowserReconciliationSettlesStoppedEvidenceWithoutReplay(t *testing.T) {
	for _, stopped := range []string{"cancelled", "failed"} {
		for _, receiptStatus := range []string{"succeeded", "failed"} {
			t.Run(stopped+"/"+receiptStatus, func(t *testing.T) {
				f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
				run := f.run(t)
				command := f.dispatch(t)
				accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, f.session.Generation)
				if err != nil || !accepted {
					t.Fatalf("begin: %v %v", accepted, err)
				}
				if stopped == "cancelled" {
					if _, err = f.h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
						t.Fatal(err)
					}
					run, err = f.h.Drive(t.Context(), run.RunID, "alice")
				} else {
					run, err = f.h.Store.Claim(run.RunID, "alice", f.h.Settings.LeaseSeconds)
					if err != nil {
						t.Fatal(err)
					}
					token := run.LeaseToken

					// Seed a failed durable checkpoint through the store boundary;
					// reconciliation must preserve this terminal state.
					run.State["runtime"].(map[string]any)["status"] = "failed"
					run.State["runtime"].(map[string]any)["error_code"] = "model_error"
					run, err = f.h.Store.Checkpoint(run.RunID, "alice", token, run.State, "failed", nil, nil, nil, []agentcontract.JSON{{"kind": "run_failed"}})
					if releaseErr := f.h.Store.Release(run.RunID, "alice", token); releaseErr != nil {
						t.Fatal(releaseErr)
					}
				}
				if err != nil || run.Status != stopped {
					t.Fatalf("stop: %s %v", run.Status, err)
				}
				var result agentcontract.JSON
				code := "handler_rejected"
				if receiptStatus == "succeeded" {
					result, code = agentcontract.JSON{"page": "orders"}, ""
				}
				if _, err = f.w.CompleteBrowserCommand("alice", command.ID, f.key, f.session.Generation, receiptStatus, result, code); err != nil {
					t.Fatal(err)
				}
				if _, err = f.w.ReconcileBrowserCommand(t.Context(), "alice", command.ID, run.Revision+1); agentcontract.ErrorCode(err) != "revision_conflict" {
					t.Fatal(err)
				}
				originalRevision := run.Revision
				settled, err := f.w.ReconcileBrowserCommand(t.Context(), "alice", command.ID, originalRevision)
				if err != nil || settled.Status != stopped || settled.NextWakeAt != nil {
					t.Fatal(settled, err)
				}
				state, err := f.h.Restore(settled)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, item := range state.Pending {
					if item.InvocationID == command.ID {
						found = item.Reconciled && item.Status == receiptStatus && item.Receipt != nil && item.Receipt.Reconciled && item.Receipt.OperationStatus == receiptStatus && item.Receipt.FactID != ""
					}
				}
				if !found {
					t.Fatal("verified receipt missing from invocation evidence", state.Pending)
				}
				latest := state.Facts[len(state.Facts)-1]
				if latest.SourceCapability != "ui.command_status" || latest.Quality != "verified_reconciliation" {
					t.Fatal("completion evidence changed source", latest)
				}
				retry, err := f.w.ReconcileBrowserCommand(t.Context(), "alice", command.ID, originalRevision)
				if err != nil || retry.Revision != settled.Revision {
					t.Fatal("lost acknowledgement retry changed evidence", retry, err)
				}
				if ok, _, beginErr := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, f.session.Generation); ok || agentcontract.ErrorCode(beginErr) != "browser_run_cancelled" {
					t.Fatal(ok, beginErr)
				}
				driven, err := f.h.Drive(t.Context(), run.RunID, "alice")
				if err != nil || driven.Status != stopped {
					t.Fatal("settlement resumed stopped run", driven.Status, err)
				}
				var count int
				if err := f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_commands").Scan(&count); err != nil || count != 1 {
					t.Fatal(count, err)
				}
			})
		}
	}
}

func TestBrowserReconciliationRechecksGrants(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	run := f.run(t)
	command := f.dispatch(t)
	if ok, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := f.h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	run, err := f.h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "succeeded", agentcontract.JSON{"page": "orders"}, ""); err != nil {
		t.Fatal(err)
	}
	user := f.d.Config.Users["alice"]
	user.BrowserActions = nil
	f.d.Config.Users["alice"] = user
	if _, err := f.w.ReconcileBrowserCommand(t.Context(), "alice", command.ID, run.Revision); agentcontract.ErrorCode(err) != "capability_not_granted" {
		t.Fatal(err)
	}
}
