package host

import (
	"context"
	"database/sql"
	"errors"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

// SetContextPolicy journals requests independently of an active driver's checkpoint. Application
// occurs before a model decision, and completion cannot overtake acceptance.
func (h *AgentHost) SetContextPolicy(ctx context.Context, id, owner, requestID string, revision int, policy agentcontract.ContextPolicy) (run agentcontract.StoredRun, err error) {
	run, err = h.Get(ctx, id, owner)
	if err != nil {
		return
	}
	if _, err = h.Policy(ctx, owner, run.PackID, true); err != nil {
		return
	}
	if !agentcontract.ValidUUID(requestID) || revision < 0 {
		return run, agentcontract.NewHostError("context_policy_invalid")
	}
	if err = policy.Validate(); err != nil {
		return
	}
	policyRaw, err := agentcontract.CanonicalJSON(policy)
	if err != nil {
		return run, agentcontract.NewHostError("context_policy_invalid")
	}
	err = h.Store.Transaction(func(tx *sql.Tx) error {
		var e error
		run, e = runstore.Owned(tx, id, owner)
		if e != nil {
			return e
		}
		var previous string
		e = tx.QueryRow("SELECT event_json->>'policy_json' FROM events WHERE run_id=? AND event_json->>'kind'='context_policy_requested' AND event_json->>'request_id'=?", id, requestID).Scan(&previous)
		if e == nil {
			if previous != string(policyRaw) {
				return agentcontract.NewHostError("context_policy_request_conflict")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if run.Revision != revision {
			return agentcontract.NewHostError("revision_conflict")
		}
		if run.CancelRequested || (run.Status != "queued" && run.Status != "running" && run.Status != "waiting" && run.Status != "needs_approval") {
			return agentcontract.NewHostError("context_policy_not_available")
		}
		var pending int
		if e = tx.QueryRow("SELECT count(*) FROM events WHERE run_id=? AND sequence>? AND event_json->>'kind'='context_policy_requested'", id, runstore.ContextPolicyCursor(run.State)).Scan(&pending); e != nil {
			return e
		}
		if pending >= 32 {
			return agentcontract.NewHostError("context_policy_queue_full")
		}
		raw, e := agentcontract.CanonicalJSON(agentcontract.JSON{"kind": "context_policy_requested", "request_id": requestID, "policy_json": string(policyRaw), "policy": policy})
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO events(run_id,created_at,event_json) VALUES(?,?,?)", id, h.Store.Now(), string(raw)); e != nil {
			return e
		}
		if run.LeaseUntil == nil || *run.LeaseUntil <= h.Store.Now() {
			if _, e = tx.Exec("UPDATE runs SET status='queued',next_wake_at=?,updated_at=? WHERE run_id=?", h.Store.Now(), h.Store.Now(), id); e != nil {
				return e
			}
		}
		run, e = runstore.Owned(tx, id, owner)
		return e
	})
	return
}

func (h *AgentHost) applyContextPolicy(run agentcontract.StoredRun, state *agentcontract.RuntimeState) (agentcontract.StoredRun, error) {
	rows, err := h.Store.DB.Query("SELECT sequence,event_json->>'policy_json' FROM events WHERE run_id=? AND sequence>? AND event_json->>'kind'='context_policy_requested' ORDER BY sequence LIMIT 32", run.RunID, state.ContextPolicyCursor)
	if err != nil {
		return run, err
	}
	var sequence int
	var policy agentcontract.ContextPolicy
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
	return h.save(run, state, "", nil, agentcontract.JSON{"kind": "context_policy_applied", "request_sequence": sequence, "policy": policy})
}
