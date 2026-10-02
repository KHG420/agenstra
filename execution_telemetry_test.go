package agenstra

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecutionTelemetryTracksModelToolAndInputWait(t *testing.T) {
	p := &hostProvider{}
	m := &hostModel{decisions: []Decision{callDecision("records.get"), {Kind: "request_input", Field: "where", Prompt: "where?"}}}
	h := testHost(t, testStore(t), p, m)
	run := createTestHostRun(t, h)
	m.hook = func(ContextPacket) {
		v, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
		if err != nil || v.Execution.Stage != "model_decision" {
			t.Fatalf("%+v %v", v.Execution, err)
		}
	}
	p.hook = func(context.Context, string, JSON, *InvocationContext) (CapabilityResult, error) {
		v, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
		if err != nil || v.Execution.Stage != "tool_execution" || len(v.Execution.Active) != 1 {
			t.Fatalf("%+v %v", v.Execution, err)
		}
		return CapabilityResult{Data: JSON{"ok": true}}, nil
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := h.GetTelemetry(t.Context(), run.RunID, "alice")
	if v.Execution.Stage != "needs_input" || v.Execution.WaitReason == nil || v.Execution.StartedAt == nil {
		t.Fatalf("%+v", v.Execution)
	}
}

func TestMemoryExtractionTelemetryIsActiveBeforeDecision(t *testing.T) {
	m := &memoryTestModel{hostModel: &hostModel{}}
	h, _ := memoryTestHost(t, m)
	run := createTestHostRun(t, h)
	m.extract = func(MemoryExtractionRequest) ([]MemoryProposal, error) {
		v, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
		if err != nil || v.Execution.Stage != "memory_extraction" || v.Execution.StartedAt == nil || v.Budget.Tokens.ReservedTokens <= 0 || v.Budget.Tokens.UnknownTokens != 0 {
			t.Fatalf("execution=%+v tokens=%+v err=%v", v.Execution, v.Budget.Tokens, err)
		}
		return nil, nil
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || len(m.inputs) != 1 {
		t.Fatalf("run=%s inputs=%v err=%v", run.Status, m.inputs, err)
	}
}

func TestModelRetryProgressAndObserverFailure(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests%2 == 1 {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": `{"kind":"final","answer_markdown":"hello","fact_ids":[]}`}}}})
	}))
	defer server.Close()
	m, _ := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	m.RetryBaseDelay = time.Nanosecond
	var progress []ModelRequestProgress
	ctx := WithModelRequestObserver(t.Context(), func(p ModelRequestProgress) error { progress = append(progress, p); return nil })
	if _, err := m.Decide(ctx, ContextPacket{}, "hello"); err != nil {
		t.Fatal(err)
	}
	if len(progress) != 2 || progress[0].Kind != "model_retry_wait" || progress[0].RetryAt == 0 || progress[1].Attempt != 2 {
		t.Fatal(progress)
	}
	ctx = WithModelRequestObserver(t.Context(), func(ModelRequestProgress) error { return errors.New("checkpoint failed") })
	if _, err := m.Decide(ctx, ContextPacket{}, "hello"); err == nil || requests != 3 {
		t.Fatal(err, requests)
	}
}

func TestRuntimeInfoAndFrozenConversationSelection(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	s := &HTTPServer{Host: f.h, Web: f.w, Deployment: f.d}
	token, _ := f.w.MintSession("alice")
	req := httptest.NewRequest("GET", "/web/v1/integrations/records/runtime-info", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	s.webHTTP(out, req)
	if out.Code != 200 || strings.Contains(out.Body.String(), "alice-key") {
		t.Fatal(out.Code, out.Body.String())
	}
	f.h.Settings.MaxConversationHistoryMessages = 1
	f.h.Settings.MaxConversationHistoryCharacters = 10
	messages := []ChatMessage{{ID: "old-1", ConversationID: "c", RunID: NewID(), Status: "completed", Text: strings.Repeat("x", 20), AnswerMarkdown: strings.Repeat("y", 20)}, {ID: "old-2", ConversationID: "c", RunID: NewID(), Status: "completed", Text: strings.Repeat("x", 20), AnswerMarkdown: strings.Repeat("y", 20)}}
	instruction, selection, err := f.w.conversationInstructionSelection("alice", messages, ChatMessage{ID: "new", ConversationID: "c", Text: "current"})
	if err != nil || selection.IncludedMessages != 1 || selection.OmittedMessages != 1 || selection.TruncatedParts != 2 || !strings.Contains(instruction, "current") {
		t.Fatal(selection, err)
	}
}
