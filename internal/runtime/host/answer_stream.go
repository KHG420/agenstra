package host

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type answerKey struct{ owner, run string }
type answerCandidate struct {
	update     agentcontract.AnswerUpdate
	workflowID string
	lease      string
	updated    time.Time
	active     context.Context
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
	runtime, _ := run.State["runtime"].(map[string]any)
	parent, _ := runtime["workflow_parent"].(map[string]any)
	if step, _ := parent["step_id"].(string); step != "" {
		entry.workflowID, _ = parent["run_id"].(string)
	}
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
		if update.Phase != "waiting" || len(update.ReasoningPreview) > agentcontract.MaxReasoningPreviewBytes {
			update.ReasoningPreview = ""
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if ctx.Err() != nil || a.entries[key] != entry {
			return
		}
		entry.update = agentcontract.AnswerUpdate{RunID: key.run, Text: strings.Clone(update.Text), Phase: update.Phase,
			ReasoningPreview: strings.Clone(update.ReasoningPreview)}
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
	return h.answerSnapshot(ctx, id, owner, false)
}

// AnswerSnapshotWithReasoningPreview includes the latest transient excerpt when
// the producing host allowed it. It shares the ordinary owner and lease checks.
func (h *AgentHost) AnswerSnapshotWithReasoningPreview(ctx context.Context, id, owner string) (agentcontract.AnswerUpdate, error) {
	return h.answerSnapshot(ctx, id, owner, true)
}

func (h *AgentHost) answerSnapshot(ctx context.Context, id, owner string, preview bool) (agentcontract.AnswerUpdate, error) {
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
	if candidate, ok := h.answers.snapshot(run, h.Now()); ok {
		update = candidate
	}
	if !preview {
		update.ReasoningPreview = ""
	} else if run.Status == "waiting" && update.Phase == "waiting" && update.ReasoningPreview == "" {
		update.ReasoningPreview = h.delegatedReasoningPreview(ctx, run)
	}
	return update, nil
}

func (a *answerStreams) snapshot(run agentcontract.StoredRun, now float64) (agentcontract.AnswerUpdate, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.entries[answerKey{run.OwnerID, run.RunID}]
	if entry == nil || entry.active.Err() != nil || entry.lease != run.LeaseToken ||
		run.LeaseUntil == nil || *run.LeaseUntil <= now || time.Since(entry.updated) > 10*time.Minute {
		return agentcontract.AnswerUpdate{}, false
	}
	return entry.update, true
}

// Read the existing bounded stream cache before looking up any child run.
func (a *answerStreams) thinkingChild(owner, workflow string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var child string
	var latest time.Time
	for key, entry := range a.entries {
		if key.owner == owner && entry.workflowID == workflow && entry.update.ReasoningPreview != "" &&
			entry.active.Err() == nil && entry.updated.After(latest) {
			child, latest = key.run, entry.updated
		}
	}
	return child
}

func (h *AgentHost) delegatedReasoningPreview(ctx context.Context, run agentcontract.StoredRun) string {
	runtime, _ := run.State["runtime"].(map[string]any)
	pending, _ := runtime["pending"].([]any)
	for _, value := range pending {
		item, _ := value.(map[string]any)
		call, _ := item["call"].(map[string]any)
		invocation, _ := item["invocation_id"].(string)
		if item["status"] != "waiting" || call["capability"] != "workflows.start" || invocation == "" {
			continue
		}
		workflowID := requestRunID(run.OwnerID, "workflow:"+invocation)
		childID := h.answers.thinkingChild(run.OwnerID, workflowID)
		if childID == "" || ctx.Err() != nil {
			continue
		}
		workflow, err := h.Store.GetRun(workflowID, run.OwnerID)
		if err != nil || !workflowPreviewActive(workflow, run.PackID) {
			continue
		}
		origin, err := workflowAgentScope(workflow)
		if err != nil || origin.RunID != run.RunID {
			continue
		}
		child, err := h.Store.GetRun(childID, run.OwnerID)
		if err != nil || !workflowPreviewActive(child, run.PackID) {
			continue
		}
		step, err := workflowAgentScope(child)
		if err != nil || step.RunID != workflowID || step.StepID == "" {
			continue
		}
		policy, err := h.ProjectPolicy(ctx, child)
		if err != nil || !policy.AllowModelData || slices.ContainsFunc(step.Capabilities, func(name string) bool {
			return !policy.GrantedCapabilities[name]
		}) {
			continue
		}
		if preview, ok := h.answers.snapshot(child, h.Now()); ok && preview.ReasoningPreview != "" {
			return preview.ReasoningPreview
		}
	}
	return ""
}

func workflowPreviewActive(run agentcontract.StoredRun, pack string) bool {
	return run.PackID == pack && !run.CancelRequested && !Terminal(run.Status) && !strings.HasPrefix(run.Status, "needs_")
}
