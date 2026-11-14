package service

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestConversationHistoryRetainsBoundedOwnerScopedEvidenceReferences(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	conversation, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.w.SubmitMessage(t.Context(), "alice", conversation.ID, "source", "Generate a report", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.w.advanceConversation(t.Context(), "alice", conversation.ID); err != nil {
		t.Fatal(err)
	}
	observations := []any{agentcontract.JSON{"status": "failed", "fact_id": "failed-reference", "capability": "records.failed"}}
	for i := range 55 {
		observations = append(observations, agentcontract.JSON{"status": "succeeded", "fact_id": fmt.Sprintf("10000000-0000-4000-8000-%012d", i), "capability": "records.calculate", "arguments": agentcontract.JSON{"private": "original arguments stay in their retained source"}})
	}
	state, err := agentcontract.CanonicalJSON(agentcontract.JSON{"runtime": agentcontract.JSON{"followups": []string{"source input"}, "observations": observations}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.Store.DB.ExecContext(t.Context(), "UPDATE runs SET state_json=$1 WHERE owner_id=$2 AND run_id=$3", string(state), "alice", m.RunID); err != nil {
		t.Fatal(err)
	}
	m.AnswerMarkdown = "Original report delivered."
	parts, err := f.w.conversationParts(t.Context(), "alice", m)
	if err != nil || len(parts) != 4 || parts[2].Field != "answer_markdown" || parts[3].Role != "reference" || parts[3].Field != "evidence" {
		t.Fatal("history lost original field positions or retained evidence", parts, err)
	}
	var reference struct {
		RunID string               `json:"run_id"`
		Facts []agentcontract.JSON `json:"facts"`
	}
	if err = json.Unmarshal([]byte(parts[3].Text), &reference); err != nil || reference.RunID != m.RunID || len(reference.Facts) != 50 {
		t.Fatal("history evidence lost its original run or read bound", reference, err)
	}
	if strings.Contains(parts[3].Text, "failed-reference") || strings.Contains(parts[3].Text, "original arguments") || reference.Facts[0]["capability"] != "records.calculate" {
		t.Fatal("history evidence exposed failed references or original arguments", parts[3])
	}
	m.Status = "completed"
	current := m
	current.ID, current.Text = "next-message", "Export another format"
	instruction, _, err := f.w.conversationInstructionSelection(t.Context(), "alice", []agentcontract.ChatMessage{m}, current)
	if err != nil || !strings.Contains(instruction, m.RunID) || !strings.Contains(instruction, reference.Facts[0]["fact_id"].(string)) || strings.Contains(instruction, "original arguments") {
		t.Fatal("bounded legacy context lost the original evidence reference", instruction, err)
	}
	parts, err = f.w.conversationParts(t.Context(), "bob", m)
	if err != nil || len(parts) != 2 {
		t.Fatal("another owner acquired retained run evidence", parts, err)
	}
}

func TestConversationContextLoadsHistoryAndInputFromFrameworkCheckpoints(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: []agentcontract.Decision{{Schema: "agenstra.decision.v1", Kind: "request_input", Field: "destination", Prompt: "Where?"}}}, false)
	a, e := f.w.CreateConversation(t.Context(), "alice", "records")
	if e != nil {
		t.Fatal(e)
	}
	b, e := f.w.CreateConversation(t.Context(), "alice", "records")
	if e != nil {
		t.Fatal(e)
	}
	first, e := f.w.SubmitMessage(t.Context(), "alice", a.ID, "first", "Ship this order", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.w.advanceConversation(t.Context(), "alice", a.ID); e != nil {
		t.Fatal(e)
	}
	run, e := f.h.Drive(t.Context(), first.RunID, "alice")
	if e != nil || run.Status != "needs_input" {
		t.Fatal(run, e)
	}
	if _, e = f.h.SupplyInput(t.Context(), run.RunID, "alice", "destination", "Shanghai", run.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.h.Drive(t.Context(), run.RunID, "alice"); e != nil {
		t.Fatal(e)
	}
	if e = f.w.advanceConversation(t.Context(), "alice", a.ID); e != nil {
		t.Fatal(e)
	}
	other, e := f.w.SubmitMessage(t.Context(), "alice", b.ID, "other", "Unrelated private task", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.w.advanceConversation(t.Context(), "alice", b.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.h.Drive(t.Context(), other.RunID, "alice"); e != nil {
		t.Fatal(e)
	}
	second, e := f.w.SubmitMessage(t.Context(), "alice", a.ID, "second", "Continue this order", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.w.advanceConversation(t.Context(), "alice", a.ID); e != nil {
		t.Fatal(e)
	}
	var frozen agentcontract.ChatMessage
	if e = webLoad(f.w.Store.store.DB, "web_messages", second.ID, "alice", &frozen); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(frozen.Instruction, "Ship this order") || !strings.Contains(frozen.Instruction, "destination: Shanghai") || strings.Contains(frozen.Instruction, "Unrelated private task") {
		t.Fatal("incorrect selected-conversation context", frozen.Instruction)
	}

	// Reopen the integration and reconstruct the host without any client-supplied
	// history. A changed historical projection must not rewrite a published input.
	if e = f.w.Store.store.Transaction(func(tx *sql.Tx) error {
		var old agentcontract.ChatMessage
		if e := webLoad(tx, "web_messages", first.ID, "alice", &old); e != nil {
			return e
		}
		old.AnswerMarkdown = "A later historical projection"
		return webSave(tx, "web_messages", old.ID, old)
	}); e != nil {
		t.Fatal(e)
	}
	if err := f.w.Close(); err != nil {
		t.Error(err)
	}
	h := testHost(t, f.h.Store, f.p, &hostModel{})
	w, e := NewWebIntegration(h, f.d, f.w.Config)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(w.Close)
	_, messages, e := w.Conversation(t.Context(), "alice", a.ID)
	if e != nil || len(messages) != 2 || messages[1].Instruction != "" {
		t.Fatal(messages, e)
	}
	run, e = h.Get(t.Context(), second.RunID, "alice")
	if e != nil {
		t.Fatal(e)
	}
	state, e := h.Restore(run)
	if e != nil || state.Instruction != frozen.Instruction {
		t.Fatal("restoration replaced the framework checkpoint", state, e)
	}
}

func TestHeadlessChatHTTPBoundary(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
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
	ticket, e := s.Web.MintSession("alice")
	if e != nil {
		t.Fatal(e)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+ticket)
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, r)
		return out
	}
	c, e := s.Web.CreateConversation(t.Context(), "alice", "records")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Web.CreateConversation(t.Context(), "alice", "records-web"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Web.CreateConversation(t.Context(), "bob", "records"); e != nil {
		t.Fatal(e)
	}
	out := request("GET", "/chat/v1/conversations?integration_id=records", "")
	var conversations []agentcontract.ChatConversation
	if e = json.Unmarshal(out.Body.Bytes(), &conversations); out.Code != 200 || e != nil || len(conversations) != 1 || conversations[0].ID != c.ID {
		t.Fatal(out.Code, out.Body.String(), e)
	}
	path := "/chat/v1/conversations/" + c.ID + "/messages"
	for _, extra := range []string{`"context":{}`, `"context_id":"chosen"`, `"history":[]`, `"instruction":"replacement"`} {
		if out = request("POST", path, `{"client_id":"first","text":"Hello",`+extra+`}`); out.Code != 422 {
			t.Fatal("host could inject framework context", out.Code, out.Body.String())
		}
	}
	body := `{"client_id":"first","text":"Hello"}`
	if out = request("POST", path, body); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	if e = s.Web.advanceConversation(t.Context(), "alice", c.ID); e != nil {
		t.Fatal(e)
	}

	// A deduplicated send and cancellation must not return internal model input.
	for _, item := range []struct{ method, path, body string }{
		{"POST", path, body},
		{"GET", "/chat/v1/conversations/" + c.ID, ""},
	} {
		out = request(item.method, item.path, item.body)
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
		var payload map[string]any
		if e = json.Unmarshal(out.Body.Bytes(), &payload); e != nil {
			t.Fatal(e)
		}
		if _, exists := payload["instruction"]; exists {
			t.Fatal("chat response exposed model input", payload)
		}
		if messages, ok := payload["messages"].([]any); ok {
			for _, value := range messages {
				if _, exists := value.(map[string]any)["instruction"]; exists {
					t.Fatal("history exposed model input")
				}
			}
		}
	}
	_, messages, e := s.Web.Conversation(t.Context(), "alice", c.ID)
	if e != nil || len(messages) != 1 {
		t.Fatal(messages, e)
	}
	out = request("POST", "/chat/v1/messages/"+messages[0].ID+"/cancel", `{}`)
	if out.Code != 200 || strings.Contains(out.Body.String(), `"instruction"`) {
		t.Fatal("cancel exposed model input", out.Code, out.Body.String())
	}
	pageSession, key, e := s.Web.CreateBrowserSession(t.Context(), "alice", "records-web", "1", []string{"ui.navigate"})
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "/browser/v1/sessions/"+pageSession.ID+"/observation", strings.NewReader(`{"generation":1,"revision":0,"observation":{"page":"orders"}}`))
	r.Header.Set("Authorization", "Bearer "+ticket)
	r.Header.Set("X-Agenstra-Browser-Key", key)
	out = httptest.NewRecorder()
	s.Handler().ServeHTTP(out, r)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"page":"orders"`) {
		t.Fatal("page observation route failed", out.Code, out.Body.String())
	}
	if out = request("GET", "/web/assets/agenstra-client.js", ""); out.Code != 200 {
		t.Fatal("headless client unavailable", out.Code)
	}
	for _, uiDependency := range []string{"agenstra-chat.js", "createElement(", "attachShadow(", "<style"} {
		if strings.Contains(out.Body.String(), uiDependency) {
			t.Fatal("headless client loads a renderer", uiDependency)
		}
	}
	if out = request("GET", "/web/assets/agenstra-chat.js", ""); out.Code != 200 || !strings.Contains(out.Body.String(), "mountAgenstraChat") {
		t.Fatal("optional chat entry unavailable", out.Code)
	}
	if out = request("POST", "/browser/v1/sessions/unused/context", `{}`); out.Code != 404 {
		t.Fatal("host context-management route remains", out.Code)
	}
}
