package engine

import (
	"context"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
)

func uncertainRun(t *testing.T) (*AgentHost, *hostProvider, StoredRun, Invocation) {
	t.Helper()
	cap := CapabilityDescription{Name: "records.get", Version: "1", InputSchema: JSON{"type": "object"}, OutputSchema: JSON{"type": "object", "properties": JSON{"confirmed": JSON{"const": true}}, "required": []any{"confirmed"}}, Effect: "write", Replay: "never"}
	p := &hostProvider{caps: map[string]CapabilityDescription{cap.Name: cap}, hook: func(context.Context, string, JSON, *InvocationContext) (CapabilityResult, error) {
		return CapabilityResult{}, errors.New("lost response after possible commit")
	}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision(cap.Name)}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_reconciliation" {
		t.Fatal(run.Status, err)
	}
	state, callErr := h.restore(run)
	if callErr != nil {
		t.Error(callErr)
	}
	return h, p, run, state.Pending[0]
}

func TestVerifiedReconciliationResumesOriginalRunWithoutReplay(t *testing.T) {
	h, p, run, item := uncertainRun(t)
	verifications := 0
	h.Reconciler = func(_ context.Context, verification ReconciliationContext) (CapabilityResult, error) {
		verifications++
		if verification.RunID != run.RunID || verification.OwnerID != "alice" || verification.Identity.IdempotencyKey != item.InvocationID || verification.Invocation.ArgumentsSHA256 != item.ArgumentsSHA256 {
			t.Error("identity or digest not bound", verification)
		}
		verification.Invocation.Call.Arguments["id"] = "changed in verifier"
		verification.Capability.OutputSchema["type"] = "string"
		return CapabilityResult{Data: JSON{"confirmed": true}}, nil
	}
	originalRevision := run.Revision
	run, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, originalRevision)
	if err != nil || run.Status != "queued" || p.calls != 1 {
		t.Fatal(run.Status, err, p.calls)
	}
	state, callErr2 := h.restore(run)
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
		if event["event"].(JSON)["kind"] == "invocation_reconciled" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing durable reconciliation event")
	}
}

func TestReconciliationRejectsStaleForgedAndUnavailableResults(t *testing.T) {
	h, p, run, item := uncertainRun(t)
	if _, err := h.Reconcile(t.Context(), run.RunID, "bob", item.InvocationID, item.ArgumentsSHA256, run.Revision); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision+1); ErrorCode(err) != "revision_conflict" {
		t.Fatal(err)
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, strings.Repeat("0", 64), run.Revision); ErrorCode(err) != "reconciliation_arguments_changed" {
		t.Fatal(err)
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", NewID(), item.ArgumentsSHA256, run.Revision); ErrorCode(err) != "reconciliation_invocation_mismatch" {
		t.Fatal(err)
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision); ErrorCode(err) != "reconciliation_unavailable" {
		t.Fatal(err)
	}
	for _, result := range []CapabilityResult{{}, {Data: JSON{"confirmed": false}}, {Data: JSON{"confirmed": math.NaN()}}, {ErrorCode: "provider_outcome_unknown"}} {
		h.Reconciler = func(context.Context, ReconciliationContext) (CapabilityResult, error) { return result, nil }
		if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision); err == nil {
			t.Fatal("unverified outcome accepted", result)
		}
	}
	current, callErr4 := h.Store.GetRun(run.RunID, "alice")
	if callErr4 != nil {
		t.Error(callErr4)
	}
	state, callErr5 := h.restore(current)
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
			h.Reconciler = func(ctx context.Context, _ ReconciliationContext) (CapabilityResult, error) {
				switch mode {
				case "authorization":
					h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) {
						return ExecutionPolicy{GrantedCapabilities: map[string]bool{}, AllowModelData: true}, nil
					}
				case "lease":
					_, err := h.Store.DB.Exec("UPDATE runs SET lease_token=? WHERE run_id=?", NewID(), run.RunID)
					if err != nil {
						t.Fatal(err)
					}
				case "cancel":
					if _, err := h.Cancel(ctx, run.RunID, "alice"); err != nil {
						t.Fatal(err)
					}
				}
				return CapabilityResult{Data: JSON{"confirmed": true}}, nil
			}
			reconciled, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
			if mode == "cancel" {
				if err != nil || reconciled.Status != "cancelled" {
					t.Fatal(reconciled.Status, err)
				}
			} else {
				if mode == "authorization" && ErrorCode(err) != "capability_not_granted" {
					t.Fatal(err)
				}
				if mode == "lease" && !errors.Is(err, ErrLeaseLost) {
					t.Fatal(err)
				}
				current, callErr6 := h.Store.GetRun(run.RunID, "alice")
				if callErr6 != nil {
					t.Error(callErr6)
				}
				state, callErr7 := h.restore(current)
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
	binding := &OperationBinding{StatusPath: []any{"status"}, IDPath: []any{"id"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}}
	cap := CapabilityDescription{Operation: binding}
	item := &Invocation{Operation: &OperationReceipt{OperationID: "job-1"}}
	for _, result := range []CapabilityResult{{Data: JSON{"id": "job-1", "status": "running"}}, {Data: JSON{"id": "job-2", "status": "succeeded"}}} {
		if err := validateReconciledResult(result, cap, item); err == nil {
			t.Fatal("unfinished or unrelated job accepted", result)
		}
	}
	if err := validateReconciledResult(CapabilityResult{Data: JSON{"id": "job-1", "status": "succeeded"}}, cap, item); err != nil {
		t.Fatal(err)
	}
}

func TestReconciliationSettlesVerifiedFailureAndKeepsOtherUncertainCallsPaused(t *testing.T) {
	h, p, run, item := uncertainRun(t)
	claimed, callErr8 := h.Store.Claim(run.RunID, "alice", 60)
	if callErr8 != nil {
		t.Error(callErr8)
	}
	state, callErr9 := h.restore(claimed)
	if callErr9 != nil {
		t.Error(callErr9)
	}
	other := item
	other.Call.CallRef = "second-write"
	other.InvocationID = deterministicInvocationID(run.RunID, other.Call.CallRef)
	state.Pending = append(state.Pending, other)
	run, err := h.save(claimed, state, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Release(run.RunID, "alice", claimed.LeaseToken); err != nil {
		t.Error(err)
	}
	h.Reconciler = func(context.Context, ReconciliationContext) (CapabilityResult, error) {
		return CapabilityResult{ErrorCode: "verified_not_committed"}, nil
	}
	run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
	if err != nil || run.Status != "needs_reconciliation" {
		t.Fatal(run.Status, err)
	}
	var callErr10 error
	state, callErr10 = h.restore(run)
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

func TestReconciliationHTTPDoesNotTrustClientOutcome(t *testing.T) {
	h, _, run, item := uncertainRun(t)
	s := &HTTPServer{Host: h}
	body := JSON{"invocation_id": item.InvocationID, "arguments_sha256": item.ArgumentsSHA256, "revision": run.Revision}
	request := func(payload JSON) *httptest.ResponseRecorder {
		raw, callErr11 := CanonicalJSON(payload)
		if callErr11 != nil {
			t.Error(callErr11)
		}
		req := httptest.NewRequest("POST", "/runs/"+run.RunID+"/reconcile", strings.NewReader(string(raw)))
		out := httptest.NewRecorder()
		s.runHTTP(out, req, "alice")
		return out
	}
	if out := request(body); out.Code != 503 {
		t.Fatal(out.Code, out.Body.String())
	}
	body["data"] = JSON{"confirmed": true}
	if out := request(body); out.Code != 422 {
		t.Fatal("client result accepted", out.Code, out.Body.String())
	}
	delete(body, "data")
	h.Reconciler = func(context.Context, ReconciliationContext) (CapabilityResult, error) {
		return CapabilityResult{Data: JSON{"confirmed": true}}, nil
	}
	if out := request(body); out.Code != 200 || strings.Contains(out.Body.String(), "lease_token") {
		t.Fatal(out.Code, out.Body.String())
	}
}
