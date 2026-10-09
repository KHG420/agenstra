package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRuntimeInfoAndFrozenConversationSelection(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	s := &HTTPServer{Host: f.h, Web: f.w, Deployment: f.d}
	token, callErr4 := f.w.MintSession("alice")
	if callErr4 != nil {
		t.Error(callErr4)
	}
	req := httptest.NewRequest("GET", "/web/v1/integrations/records/runtime-info", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	s.webHTTP(out, req)
	if out.Code != 200 || strings.Contains(out.Body.String(), "alice-key") {
		t.Fatal(out.Code, out.Body.String())
	}
	f.h.Settings.MaxConversationHistoryMessages = 1
	f.h.Settings.MaxConversationHistoryCharacters = 10
	messages := []agentcontract.ChatMessage{{ID: "old-1", ConversationID: "c", RunID: agentcontract.NewID(), Status: "completed", Text: strings.Repeat("x", 20), AnswerMarkdown: strings.Repeat("y", 20)}, {ID: "old-2", ConversationID: "c", RunID: agentcontract.NewID(), Status: "completed", Text: strings.Repeat("x", 20), AnswerMarkdown: strings.Repeat("y", 20)}}
	instruction, selection, err := f.w.conversationInstructionSelection("alice", messages, agentcontract.ChatMessage{ID: "new", ConversationID: "c", Text: "current"})
	if err != nil || selection.IncludedMessages != 1 || selection.OmittedMessages != 1 || selection.TruncatedParts != 2 || !strings.Contains(instruction, "current") {
		t.Fatal(selection, err)
	}
}
