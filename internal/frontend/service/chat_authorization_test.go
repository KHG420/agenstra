package service

import (
	"context"
	"fmt"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestConversationAuthorizesHistoryWithinOneRequest(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	conversation, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	var firstRun string
	for index := 0; index < 8; index++ {
		message, err := f.w.SubmitMessage(t.Context(), "alice", conversation.ID, fmt.Sprintf("message-%d", index), "Read a record", "")
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			firstRun = message.RunID
		}
		if err := f.w.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if run, err := f.h.Drive(t.Context(), message.RunID, "alice"); err != nil || run.Status != "completed" {
			t.Fatal("history run did not finish", run.Status, err)
		}
		if err := f.w.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	original := f.h.PolicyResolver
	checks := 0
	revoked := false
	f.h.PolicyResolver = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
		checks++
		if revoked || pack == "archived-private" {
			return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("access_denied")
		}
		return original(ctx, owner, pack)
	}
	_, messages, err := f.w.Conversation(t.Context(), "alice", conversation.ID)
	if err != nil || len(messages) != 8 {
		t.Fatal("history read lost messages", len(messages), err)
	}
	if checks != 1 {
		t.Fatalf("one history response repeated authorization %d times", checks)
	}
	for _, message := range messages {
		if message.AnswerMarkdown != "Done" || message.Instruction != "" {
			t.Fatal("history response changed saved content", message)
		}
	}
	checks, revoked = 0, true
	if _, messages, err := f.w.Conversation(t.Context(), "alice", conversation.ID); agentcontract.ErrorCode(err) != "access_denied" || messages != nil || checks != 1 {
		t.Fatal("next request did not refresh current access", checks, err)
	}
	checks, revoked = 0, false
	if _, err := f.h.Store.DB.Exec("UPDATE runs SET pack_id='archived-private' WHERE run_id=?", firstRun); err != nil {
		t.Fatal(err)
	}
	if _, messages, err := f.w.Conversation(t.Context(), "alice", conversation.ID); agentcontract.ErrorCode(err) != "access_denied" || messages != nil || checks != 2 {
		t.Fatal("historical run from another pack skipped authorization", checks, err)
	}
}
