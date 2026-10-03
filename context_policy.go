package agenstra

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/jsonvalue"
)

// ContextPolicy sets optional thresholds for projecting model input within existing budgets.
type ContextPolicy struct {
	TriggerRatio float64 `json:"trigger_ratio"`
	TargetRatio  float64 `json:"target_ratio"`
}

// Validate checks projection ratios without changing execution limits.
func (p ContextPolicy) Validate() error {
	if math.IsNaN(p.TriggerRatio) || math.IsNaN(p.TargetRatio) || math.IsInf(p.TriggerRatio, 0) || math.IsInf(p.TargetRatio, 0) || p.TriggerRatio < 0 || p.TriggerRatio > 1 || p.TargetRatio < 0 || p.TargetRatio > p.TriggerRatio || (p.TriggerRatio > 0 && p.TargetRatio == 0) {
		return hostError("context_policy_invalid")
	}
	return nil
}
func (r *AgentRuntime) contextPolicy(state *RuntimeState) ContextPolicy {
	if state.ContextPolicy != nil {
		return *state.ContextPolicy
	}
	return r.ContextPolicy
}
func (r *AgentRuntime) characterProjection(state *RuntimeState, packet ContextPacket, prompt string) (ContextPacket, string, bool) {
	policy := r.contextPolicy(state)
	limit := r.MaxContextCharacters
	promptSize := utf8.RuneCountInString(prompt)
	size := contextCharacters(packet)
	if size <= math.MaxInt-promptSize {
		size += promptSize
	}
	goal := limit
	reason := "none"
	if size > limit {
		reason = "hard_limit"
	}
	info := modelInfo(r.Model)
	tokensKnown := r.MaxModelInputTokens > 0 || r.ModelContextWindowTokens > 0 || info.MaxInputTokens != nil || info.ContextWindowTokens != nil
	if !tokensKnown && policy.TriggerRatio > 0 && float64(size) >= float64(limit)*policy.TriggerRatio {
		goal = int(float64(limit) * policy.TargetRatio)
		if reason == "none" {
			reason = "soft_threshold"
		}
	}
	projected := budgetContext(packet, state, goal-utf8.RuneCountInString(prompt))
	return projected, reason, contextCharacters(projected) <= goal-promptSize
}

func contextPolicyCursor(envelope JSON) int {
	runtime, _ := envelope["runtime"].(JSON)
	cursor, _ := jsonvalue.Index(runtime["context_policy_cursor"])
	return cursor
}
func latestContextPolicy(q sqlQueryer, id string) (int, error) {
	var seq int
	err := q.QueryRow("SELECT coalesce(max(sequence),0) FROM events WHERE run_id=? AND event_json->>'kind'='context_policy_requested'", id).Scan(&seq)
	return seq, err
}

// SetContextPolicy journals requests independently of an active driver's checkpoint. Application
// occurs before a model decision, and completion cannot overtake acceptance.
func (h *AgentHost) SetContextPolicy(ctx context.Context, id, owner, requestID string, revision int, policy ContextPolicy) (run StoredRun, err error) {
	run, err = h.Get(ctx, id, owner)
	if err != nil {
		return
	}
	if _, err = h.policy(ctx, owner, run.PackID, true); err != nil {
		return
	}
	if !validUUID(requestID) || revision < 0 {
		return run, hostError("context_policy_invalid")
	}
	if err = policy.Validate(); err != nil {
		return
	}
	policyRaw, err := CanonicalJSON(policy)
	if err != nil {
		return run, hostError("context_policy_invalid")
	}
	err = h.Store.write(func(tx *sql.Tx) error {
		var e error
		run, e = owned(tx, id, owner)
		if e != nil {
			return e
		}
		var previous string
		e = tx.QueryRow("SELECT event_json->>'policy_json' FROM events WHERE run_id=? AND event_json->>'kind'='context_policy_requested' AND event_json->>'request_id'=?", id, requestID).Scan(&previous)
		if e == nil {
			if previous != string(policyRaw) {
				return hostError("context_policy_request_conflict")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if run.Revision != revision {
			return hostError("revision_conflict")
		}
		if run.CancelRequested || (run.Status != "queued" && run.Status != "running" && run.Status != "waiting" && run.Status != "needs_approval") {
			return hostError("context_policy_not_available")
		}
		var pending int
		if e = tx.QueryRow("SELECT count(*) FROM events WHERE run_id=? AND sequence>? AND event_json->>'kind'='context_policy_requested'", id, contextPolicyCursor(run.State)).Scan(&pending); e != nil {
			return e
		}
		if pending >= 32 {
			return hostError("context_policy_queue_full")
		}
		raw, e := CanonicalJSON(JSON{"kind": "context_policy_requested", "request_id": requestID, "policy_json": string(policyRaw), "policy": policy})
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO events(run_id,created_at,event_json) VALUES(?,?,?)", id, h.Store.now(), string(raw)); e != nil {
			return e
		}
		if run.LeaseUntil == nil || *run.LeaseUntil <= h.Store.now() {
			if _, e = tx.Exec("UPDATE runs SET status='queued',next_wake_at=?,updated_at=? WHERE run_id=?", h.Store.now(), h.Store.now(), id); e != nil {
				return e
			}
		}
		run, e = owned(tx, id, owner)
		return e
	})
	return
}

func (h *AgentHost) applyContextPolicy(run StoredRun, state *RuntimeState) (StoredRun, error) {
	rows, err := h.Store.DB.Query("SELECT sequence,event_json->>'policy_json' FROM events WHERE run_id=? AND sequence>? AND event_json->>'kind'='context_policy_requested' ORDER BY sequence LIMIT 32", run.RunID, state.ContextPolicyCursor)
	if err != nil {
		return run, err
	}
	var sequence int
	var policy ContextPolicy
	for rows.Next() {
		var raw string
		if err = rows.Scan(&sequence, &raw); err != nil {
			return run, errors.Join(err, rows.Close())
		}
		if err = jsonvalue.DecodeStrict([]byte(raw), &policy); err != nil {
			return run, errors.Join(err, rows.Close())
		}
		if err = policy.Validate(); err != nil {
			return run, errors.Join(err, rows.Close())
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil || sequence == 0 {
		return run, err
	}
	state.ContextPolicy = &policy
	state.ContextPolicyCursor = sequence
	return h.save(run, state, "", nil, JSON{"kind": "context_policy_applied", "request_sequence": sequence, "policy": policy})
}
