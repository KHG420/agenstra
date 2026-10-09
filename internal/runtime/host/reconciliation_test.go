package host

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func uncertainRun(t *testing.T) (*AgentHost, *hostProvider, agentcontract.StoredRun, agentcontract.Invocation) {
	t.Helper()
	cap := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", InputSchema: agentcontract.JSON{"type": "object"}, OutputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"confirmed": agentcontract.JSON{"const": true}}, "required": []any{"confirmed"}}, Effect: "write", Replay: "never"}
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}, hook: func(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		return agentcontract.CapabilityResult{}, errors.New("lost response after possible commit")
	}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision(cap.Name)}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_reconciliation" {
		t.Fatal(run.Status, err)
	}
	state, callErr := h.Restore(run)
	if callErr != nil {
		t.Error(callErr)
	}
	return h, p, run, state.Pending[0]
}

func TestVerifiedReconciliationResumesOriginalRunWithoutReplay(t *testing.T) {
	h, p, run, item := uncertainRun(t)
	verifications := 0
	h.Reconciler = func(_ context.Context, verification agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
		verifications++
		if verification.RunID != run.RunID || verification.OwnerID != "alice" || verification.Identity.IdempotencyKey != item.InvocationID || verification.Invocation.ArgumentsSHA256 != item.ArgumentsSHA256 {
			t.Error("identity or digest not bound", verification)
		}
		verification.Invocation.Call.Arguments["id"] = "changed in verifier"
		verification.Capability.OutputSchema["type"] = "string"
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"confirmed": true}}, nil
	}
	originalRevision := run.Revision
	run, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, originalRevision)
	if err != nil || run.Status != "queued" || p.calls != 1 {
		t.Fatal(run.Status, err, p.calls)
	}
	state, callErr2 := h.Restore(run)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if len(state.Facts) != 1 || !state.Pending[0].Reconciled || state.Pending[0].Call.Arguments["id"] != "R-1" {
		t.Fatal(state)
	}
	if state.Pending[0].ErrorCode != nil || state.Pending[0].Receipt.ErrorCode != "" {
		t.Fatal("verified success retained the previous unknown outcome error")
	}
	if _, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, originalRevision); err != nil || verifications != 1 {
		t.Fatal("lost ACK reran verifier", err, verifications)
	}
	claimed, err := h.Store.Claim(run.RunID, "alice", 60)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, originalRevision); err != nil {
		t.Fatal("retry blocked by original run's active lease", err)
	}
	if err = h.Store.Release(run.RunID, "alice", claimed.LeaseToken); err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 1 {
		t.Fatal(run.Status, err, p.calls)
	}
	h.Reconciler = nil
	if _, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, originalRevision); err != nil {
		t.Fatal("retry after original run completed", err)
	}
	events, callErr3 := h.Store.ListEvents(run.RunID, "alice", 0, 100)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	found := false
	for _, event := range events {
		if event["event"].(agentcontract.JSON)["kind"] == "invocation_reconciled" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing durable reconciliation event")
	}
}

func TestReconciliationRejectsStaleForgedAndUnavailableResults(t *testing.T) {
	h, p, run, item := uncertainRun(t)
	if _, err := h.Reconcile(t.Context(), run.RunID, "bob", item.InvocationID, item.ArgumentsSHA256, run.Revision); !errors.Is(err, runstore.ErrRunNotFound) {
		t.Fatal(err)
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision+1); agentcontract.ErrorCode(err) != "revision_conflict" {
		t.Fatal(err)
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, strings.Repeat("0", 64), run.Revision); agentcontract.ErrorCode(err) != "reconciliation_arguments_changed" {
		t.Fatal(err)
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", agentcontract.NewID(), item.ArgumentsSHA256, run.Revision); agentcontract.ErrorCode(err) != "reconciliation_invocation_mismatch" {
		t.Fatal(err)
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision); agentcontract.ErrorCode(err) != "reconciliation_unavailable" {
		t.Fatal(err)
	}
	for _, result := range []agentcontract.CapabilityResult{{}, {Data: agentcontract.JSON{"confirmed": false}}, {Data: agentcontract.JSON{"confirmed": math.NaN()}}, {ErrorCode: "provider_outcome_unknown"}} {
		h.Reconciler = func(context.Context, agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
			return result, nil
		}
		if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision); err == nil {
			t.Fatal("unverified outcome accepted", result)
		}
	}
	current, callErr4 := h.Store.GetRun(run.RunID, "alice")
	if callErr4 != nil {
		t.Error(callErr4)
	}
	state, callErr5 := h.Restore(current)
	if callErr5 != nil {
		t.Error(callErr5)
	}
	if current.Status != "needs_reconciliation" || current.Revision != run.Revision || len(state.Facts) != 0 || p.calls != 1 || state.Pending[0].Reconciled {
		t.Fatal("failed verification changed state", current, state, p.calls)
	}
}

func TestReconciliationChecksFreshAuthorizationLeaseAndCancellation(t *testing.T) {
	for _, mode := range []string{"authorization", "lease", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			h, p, run, item := uncertainRun(t)
			h.Reconciler = func(ctx context.Context, _ agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
				switch mode {
				case "authorization":
					h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
						return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{}, AllowModelData: true}, nil
					}
				case "lease":
					_, err := h.Store.DB.Exec("UPDATE runs SET lease_token=? WHERE run_id=?", agentcontract.NewID(), run.RunID)
					if err != nil {
						t.Fatal(err)
					}
				case "cancel":
					if _, err := h.Cancel(ctx, run.RunID, "alice"); err != nil {
						t.Fatal(err)
					}
				}
				return agentcontract.CapabilityResult{Data: agentcontract.JSON{"confirmed": true}}, nil
			}
			reconciled, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
			if mode == "cancel" {
				if err != nil || reconciled.Status != "cancelled" {
					t.Fatal(reconciled.Status, err)
				}
			} else {
				if mode == "authorization" && agentcontract.ErrorCode(err) != "capability_not_granted" {
					t.Fatal(err)
				}
				if mode == "lease" && !errors.Is(err, runstore.ErrLeaseLost) {
					t.Fatal(err)
				}
				current, callErr6 := h.Store.GetRun(run.RunID, "alice")
				if callErr6 != nil {
					t.Error(callErr6)
				}
				state, callErr7 := h.Restore(current)
				if callErr7 != nil {
					t.Error(callErr7)
				}
				if len(state.Facts) != 0 || state.Pending[0].Reconciled {
					t.Fatal("stale verification committed", state)
				}
			}
			if p.calls != 1 {
				t.Fatal("write replayed", p.calls)
			}
		})
	}
}

func TestReconciliationRequiresSettledMatchingOperation(t *testing.T) {
	binding := &agentcontract.OperationBinding{StatusPath: []any{"status"}, IDPath: []any{"id"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}}
	cap := agentcontract.CapabilityDescription{Operation: binding}
	item := &agentcontract.Invocation{Operation: &agentcontract.OperationReceipt{OperationID: "job-1"}}
	for _, result := range []agentcontract.CapabilityResult{{Data: agentcontract.JSON{"id": "job-1", "status": "running"}}, {Data: agentcontract.JSON{"id": "job-2", "status": "succeeded"}}} {
		if err := validateReconciledResult(result, cap, item); err == nil {
			t.Fatal("unfinished or unrelated job accepted", result)
		}
	}
	if err := validateReconciledResult(agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "job-1", "status": "succeeded"}}, cap, item); err != nil {
		t.Fatal(err)
	}
}

func TestReconciliationSettlesVerifiedFailureAndKeepsOtherUncertainCallsPaused(t *testing.T) {
	h, p, run, item := uncertainRun(t)
	claimed, callErr8 := h.Store.Claim(run.RunID, "alice", 60)
	if callErr8 != nil {
		t.Error(callErr8)
	}
	state, callErr9 := h.Restore(claimed)
	if callErr9 != nil {
		t.Error(callErr9)
	}
	other := item
	other.Call.CallRef = "second-write"
	other.InvocationID = agentcontract.NewID()
	state.Pending = append(state.Pending, other)
	run, err := h.save(claimed, state, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Release(run.RunID, "alice", claimed.LeaseToken); err != nil {
		t.Error(err)
	}
	h.Reconciler = func(context.Context, agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
		return agentcontract.CapabilityResult{ErrorCode: "verified_not_committed"}, nil
	}
	run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
	if err != nil || run.Status != "needs_reconciliation" {
		t.Fatal(run.Status, err)
	}
	var callErr10 error
	state, callErr10 = h.Restore(run)
	if callErr10 != nil {
		t.Error(callErr10)
	}
	if state.Pending[0].Status != "failed" || state.Pending[1].Reconciled || len(state.Facts) != 0 {
		t.Fatal(state)
	}
	run, err = h.Reconcile(t.Context(), run.RunID, "alice", other.InvocationID, other.ArgumentsSHA256, run.Revision)
	if err != nil || run.Status != "queued" || p.calls != 1 {
		t.Fatal(run.Status, err, p.calls)
	}
}
