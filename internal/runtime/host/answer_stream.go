package host

import (
	"context"
	"strings"
	"sync"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type answerKey struct{ owner, run string }
type answerCandidate struct {
	update  agentcontract.AnswerUpdate
	lease   string
	updated time.Time
	active  context.Context
}

type answerStreams struct {
	mu      sync.Mutex
	entries map[answerKey]*answerCandidate
}

// One entry per active step. Pointer identity and the claimed lease fence callbacks
// from evicted, cancelled, superseded and expired execution contexts.
func (a *answerStreams) begin(ctx context.Context, run agentcontract.StoredRun) context.Context {
	key := answerKey{run.OwnerID, run.RunID}
	entry := &answerCandidate{update: agentcontract.AnswerUpdate{RunID: run.RunID, Phase: "waiting"}, lease: run.LeaseToken, updated: time.Now(), active: ctx}
	a.mu.Lock()
	if a.entries == nil {
		a.entries = make(map[answerKey]*answerCandidate)
	}
	for k, v := range a.entries {
		if time.Since(v.updated) > 10*time.Minute {
			delete(a.entries, k)
		}
	}
	if len(a.entries) >= 128 {
		var oldest answerKey
		var stamp time.Time
		for k, v := range a.entries {
			if stamp.IsZero() || v.updated.Before(stamp) {
				oldest, stamp = k, v.updated
			}
		}
		delete(a.entries, oldest)
	}
	a.entries[key] = entry
	a.mu.Unlock()
	return agentcontract.WithAnswerObserver(ctx, func(update agentcontract.AnswerUpdate) {
		if update.Phase != "generating" && update.Phase != "checking" && update.Phase != "waiting" {
			return
		}
		if len(update.Text) > 120000 {
			update.Text, update.Phase = "", "waiting"
		}
		if update.Phase == "waiting" {
			update.Text = ""
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if ctx.Err() != nil || a.entries[key] != entry {
			return
		}
		entry.update = agentcontract.AnswerUpdate{RunID: key.run, Text: strings.Clone(update.Text), Phase: update.Phase}
		entry.updated = time.Now()
	})
}

func (a *answerStreams) clear(owner, run, lease string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := answerKey{owner, run}
	if entry := a.entries[key]; entry != nil && (lease == "" || entry.lease == lease) {
		delete(a.entries, key)
	}
}

// AnswerSnapshot rechecks ownership and current authorization on every read.
// Drafts are transient. A completed answer is always read from durable state,
// including after process restart or a reconnect routed to another instance.
func (h *AgentHost) AnswerSnapshot(ctx context.Context, id, owner string) (agentcontract.AnswerUpdate, error) {
	run, err := h.Get(ctx, id, owner)
	if err != nil {
		return agentcontract.AnswerUpdate{}, err
	}
	update := agentcontract.AnswerUpdate{RunID: id, Phase: "waiting"}
	if run.Status == "completed" {
		runtime, _ := run.State["runtime"].(map[string]any)
		update.Text, _ = runtime["answer_markdown"].(string)
		update.Phase = "completed"
		return update, nil
	}
	if run.CancelRequested {
		update.Phase = "cancelled"
		return update, nil
	}
	if run.Status == "failed" || run.Status == "cancelled" {
		update.Phase = run.Status
		return update, nil
	}
	h.answers.mu.Lock()
	defer h.answers.mu.Unlock()
	if entry := h.answers.entries[answerKey{owner, id}]; entry != nil && entry.active.Err() == nil && entry.lease == run.LeaseToken && run.LeaseUntil != nil && *run.LeaseUntil > h.Now() && time.Since(entry.updated) <= 10*time.Minute {
		return entry.update, nil
	}
	return update, nil
}
