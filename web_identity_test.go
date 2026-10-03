package agenstra

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConversationListChecksEachCurrentIntegration(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	allowed, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	// Revoked history must not consume the visible list's 100-item limit.
	for range 101 {
		if _, err = f.w.CreateConversation(t.Context(), "alice", "records-web"); err != nil {
			t.Fatal(err)
		}
	}
	policy := f.h.PolicyResolver
	f.h.PolicyResolver = func(ctx context.Context, owner, pack string) (ExecutionPolicy, error) {
		if pack == "records-web" {
			return ExecutionPolicy{}, hostError("identity_unverified")
		}
		return policy(ctx, owner, pack)
	}
	items, err := f.w.ListConversations(t.Context(), "alice", "")
	if err != nil || len(items) != 1 || items[0].ID != allowed.ID {
		t.Fatalf("current integration history: %v %v", items, err)
	}
	if _, err = f.w.ListConversations(t.Context(), "alice", "records-web"); ErrorCode(err) != "identity_unverified" {
		t.Fatalf("filtered history: %v", err)
	}
	f.h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{}, hostError("authorization_unavailable")
	}
	if items, err = f.w.ListConversations(t.Context(), "alice", ""); ErrorCode(err) != "authorization_unavailable" || len(items) != 0 {
		t.Fatalf("identity service failure: %v %v", items, err)
	}
}

func TestBrowserHTTPRejectsRevokedIdentityBeforeSessionUse(t *testing.T) {
	for _, operation := range []string{"resume", "observation", "poll", "command"} {
		t.Run(operation, func(t *testing.T) {
			f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
			f.run(t)
			command := f.dispatch(t)
			ticket, err := f.w.MintSession("alice")
			if err != nil {
				t.Fatal(err)
			}
			f.h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) {
				return ExecutionPolicy{}, hostError("identity_unverified")
			}
			method := "POST"
			path := "/browser/v1/sessions/" + f.session.ID + "/" + operation
			body := `{"generation":1}`
			if operation == "command" {
				method, path, body = "GET", "/browser/v1/commands/"+command.ID, ""
			} else if operation == "observation" {
				body = `{"generation":1,"revision":1,"observation":{"page":"changed"}}`
			}
			r := httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+ticket)
			r.Header.Set("X-Agenstra-Browser-Key", f.key)
			out := httptest.NewRecorder()
			(&HTTPServer{Host: f.h, Web: f.w}).webHTTP(out, r)
			if out.Code != 403 || !strings.Contains(out.Body.String(), "identity_unverified") {
				t.Fatalf("revoked identity: %d %s", out.Code, out.Body.String())
			}
			var saved BrowserSession
			if err = webLoad(f.w.Store.store.DB, "web_sessions", f.session.ID, "alice", &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Generation != f.session.Generation || saved.ContextRevision != f.session.ContextRevision || saved.Context["page"] != "home" {
				t.Fatal("denied request changed session", saved)
			}
		})
	}
}

func TestBrowserReceiptAndCloseRemainAvailableAfterRevocation(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	f.run(t)
	command := f.dispatch(t)
	if ok, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	ticket, err := f.w.MintSession("alice")
	if err != nil {
		t.Fatal(err)
	}
	f.h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{}, hostError("identity_unverified")
	}
	for _, path := range []string{"commands/" + command.ID + "/result", "sessions/" + f.session.ID + "/close"} {
		body := `{"generation":1}`
		if strings.HasPrefix(path, "commands/") {
			body = `{"generation":1,"status":"succeeded","result":{"page":"orders"},"error_code":""}`
		}
		r := httptest.NewRequest("POST", "/browser/v1/"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+ticket)
		r.Header.Set("X-Agenstra-Browser-Key", f.key)
		out := httptest.NewRecorder()
		(&HTTPServer{Host: f.h, Web: f.w}).webHTTP(out, r)
		if out.Code != 200 {
			t.Fatalf("receipt/close after revocation: %d %s", out.Code, out.Body.String())
		}
		if strings.HasPrefix(path, "commands/") {
			var receipt BrowserCommand
			if err = json.Unmarshal(out.Body.Bytes(), &receipt); err != nil || receipt.Status != "succeeded" {
				t.Fatal(receipt, err)
			}
		}
	}
}
