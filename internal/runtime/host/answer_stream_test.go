package host

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestAnswerSnapshotsAreOwnedFencedAndDurable(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	run, err := h.Store.Claim(run.RunID, "alice", 30)
	if err != nil {
		t.Fatal(err)
	}
	old := h.answers.begin(t.Context(), run)
	agentcontract.ObserveAnswer(old, agentcontract.AnswerUpdate{Text: "old", Phase: "generating"})
	current := h.answers.begin(t.Context(), run)
	agentcontract.ObserveAnswer(old, agentcontract.AnswerUpdate{Text: "stale", Phase: "generating"})
	if got, err := h.AnswerSnapshot(t.Context(), run.RunID, "alice"); err != nil || got.Text != "" {
		t.Fatal("superseded candidate survived", got, err)
	}
	agentcontract.ObserveAnswer(current, agentcontract.AnswerUpdate{Text: "draft", Phase: "checking"})
	if _, err := h.AnswerSnapshot(t.Context(), run.RunID, "bob"); err == nil {
		t.Fatal("cross-owner disclosure")
	}
	if got, err := h.AnswerSnapshot(t.Context(), run.RunID, "alice"); err != nil || got.Phase != "checking" || got.Text != "draft" {
		t.Fatal(got, err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	state.Status = "completed"
	state.AnswerMarkdown = "validated answer"
	if _, err = h.save(run, state, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Reconnect to a new host instance with no transient cache.
	restarted := testHost(t, h.Store, &hostProvider{}, &hostModel{})
	if got, err := restarted.AnswerSnapshot(t.Context(), run.RunID, "alice"); err != nil || got.Text != "validated answer" || got.Phase != "completed" {
		t.Fatal("completion not durable", got, err)
	}
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("access_denied")
	}
	if got, err := h.AnswerSnapshot(t.Context(), run.RunID, "alice"); err == nil || got.Text != "" {
		t.Fatal("revoked access retained answer", got, err)
	}
}

func TestAnswerCancellationEvictionAndBounds(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	run, err := h.Store.Claim(run.RunID, "alice", 30)
	if err != nil {
		t.Fatal(err)
	}
	ctx := h.answers.begin(t.Context(), run)
	agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: "draft", Phase: "generating"})
	if _, err = h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: "late", Phase: "generating"})
	if got, err := h.AnswerSnapshot(t.Context(), run.RunID, "alice"); err != nil || got.Text != "" || got.Phase != "cancelled" {
		t.Fatal(got, err)
	}
	if len(h.answers.entries) != 0 {
		t.Fatal("cancelled candidate retained")
	}
	for i := 0; i < 140; i++ {
		h.answers.begin(t.Context(), agentcontract.StoredRun{RunID: fmt.Sprint(i), OwnerID: "alice", LeaseToken: "lease"})
	}
	if len(h.answers.entries) != 128 {
		t.Fatal("unbounded candidates", len(h.answers.entries))
	}
	run.RunID = "bounded"
	ctx = h.answers.begin(t.Context(), run)
	agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: strings.Repeat("x", 120001), Phase: "generating"})
	entry := h.answers.entries[answerKey{"alice", run.RunID}]
	if entry.update.Text != "" {
		t.Fatal("unbounded answer")
	}
	entry.updated = time.Now().Add(-11 * time.Minute)
	h.answers.begin(t.Context(), agentcontract.StoredRun{RunID: "new", OwnerID: "alice"})
	agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: "late expired text", Phase: "generating"})
	if h.answers.entries[answerKey{"alice", run.RunID}] != nil {
		t.Fatal("expired generation revived")
	}
}
