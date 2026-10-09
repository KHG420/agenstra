package service

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
)

func TestChatTickSkipsIdleConversationsWithoutWriteLock(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	if _, err := f.w.CreateConversation(t.Context(), "alice", "records"); err != nil {
		t.Fatal(err)
	}
	terminal, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	message, err := f.w.SubmitMessage(t.Context(), "alice", terminal.ID, "terminal", "Done", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	run, err := f.h.Drive(t.Context(), message.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatalf("complete run: %s %v", run.Status, err)
	}
	_, messages, err := f.w.Conversation(t.Context(), "alice", terminal.ID)
	if err != nil || len(messages) != 1 || messages[0].Status != "completed" {
		t.Fatalf("terminal conversation: %+v %v", messages, err)
	}

	path, err := filepath.Abs(f.w.Store.store.Path)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := blocker.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err = blocker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := blocker.Exec("ROLLBACK"); err != nil {
			t.Error(err)
		}
	}()
	if _, err = f.w.Store.store.DB.Exec("PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	if err = f.w.Tick(t.Context()); err != nil {
		t.Fatalf("idle Tick acquired a write lock: %v", err)
	}
}
