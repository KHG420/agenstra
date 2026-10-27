package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestConversationConditionalPollPreservesHistoryAndAuthorization(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: []agentcontract.Decision{callDecision("records.get")}}, false)
	f.p.caps = f.p.Capabilities()
	capability := f.p.caps["records.get"]
	capability.Effect, capability.Replay = "write", "idempotent"
	f.p.caps["records.get"] = capability
	s := &HTTPServer{Host: f.h, Web: f.w, Deployment: f.d}
	ticket, err := f.w.MintSession("alice")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	get := func(id, tag, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/chat/v1/conversations/"+id, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("If-None-Match", tag)
		out := httptest.NewRecorder()
		s.webHTTP(out, r)
		return out
	}
	first := get(c.ID, "", ticket)
	tag := first.Header().Get("ETag")
	if first.Code != 200 || tag == "" || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(first.Code, first.Header())
	}
	if out := get(c.ID, tag, ticket); out.Code != 304 || out.Body.Len() != 0 {
		t.Fatal("unchanged history was downloaded again", out.Code, out.Body.String())
	}
	if out := get(c.ID, tag, ""); out.Code != 401 {
		t.Fatal("validator bypassed authentication", out.Code)
	}
	foreign, err := f.w.CreateConversation(t.Context(), "bob", "records")
	if err != nil {
		t.Fatal(err)
	}
	if out := get(foreign.ID, tag, ticket); out.Code != 404 {
		t.Fatal("validator bypassed owner isolation", out.Code)
	}
	m, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, "lookup", "Read R-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if out := get(c.ID, tag, ticket); out.Code != 200 || out.Header().Get("ETag") != "" {
		t.Fatal("active work was cached", out.Code, out.Header())
	}
	run, err := f.h.Drive(t.Context(), m.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatal(run.Status, err)
	}
	completed := get(c.ID, tag, ticket)
	tag = completed.Header().Get("ETag")
	var snapshot struct {
		Messages []agentcontract.ChatMessage `json:"messages"`
	}
	if completed.Code != 200 || tag == "" || json.Unmarshal(completed.Body.Bytes(), &snapshot) != nil || len(snapshot.Messages) != 1 || snapshot.Messages[0].Run == nil {
		t.Fatal("completed evidence was omitted", completed.Code, completed.Body.String())
	}
	f.now++
	if out := get(c.ID, `"unrelated", `+tag, ticket); out.Code != 304 || out.Body.Len() != 0 {
		t.Fatal("observation clock invalidated unchanged history", out.Code)
	}
	fresh := get(c.ID, "", ticket)
	if fresh.Code != 200 || json.Unmarshal(fresh.Body.Bytes(), &snapshot) != nil || snapshot.Messages[0].Run["telemetry"].(map[string]any)["observed_at"] != f.now {
		t.Fatal("validator changed the actual telemetry response", fresh.Code, fresh.Body.String())
	}
	f.h.Settings.MaxConcurrentRuns++
	if out := get(c.ID, tag, ticket); out.Code != 200 || out.Header().Get("ETag") == tag {
		t.Fatal("changed telemetry configuration was hidden", out.Code)
	}
	r := httptest.NewRequest("OPTIONS", "/chat/v1/conversations/"+c.ID, nil)
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Access-Control-Request-Headers", "If-None-Match")
	out := httptest.NewRecorder()
	s.webHTTP(out, r)
	if out.Code != 204 || !strings.Contains(out.Header().Get("Access-Control-Allow-Headers"), "If-None-Match") || !strings.Contains(out.Header().Get("Access-Control-Expose-Headers"), "ETag") {
		t.Fatal("authorized cross-origin hosts cannot use conditional polls", out.Code, out.Header())
	}
}
