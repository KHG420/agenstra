package agenstra

import (
	"context"
	"strings"
	"time"
)

type ReconciliationContext struct {
	RunID        string
	OwnerID      string
	OriginPackID string
	Invocation   Invocation
	Identity     InvocationContext
	Capability   CapabilityDescription
}

// InvocationReconciler must verify the original invocation using authoritative
// business evidence (for example its idempotency key). It must not replay it.
// Return only a settled result; a verification failure leaves the run paused.
type InvocationReconciler func(context.Context, ReconciliationContext) (CapabilityResult, error)

type verifiedResultProvider struct {
	CapabilityProvider
	result CapabilityResult
}

func (p verifiedResultProvider) Invoke(context.Context, string, JSON, *InvocationContext) (CapabilityResult, error) {
	return p.result, nil
}

func (h *AgentHost) Reconcile(ctx context.Context, id, owner, invocationID, argsSHA string, revision int) (StoredRun, error) {
	h.InitializeDefaults()
	current, err := h.Get(ctx, id, owner)
	if err != nil {
		return current, err
	}
	if !validUUID(invocationID) || len(argsSHA) != 64 || strings.Trim(argsSHA, "0123456789abcdef") != "" || revision < 0 {
		return current, hostError("reconciliation_invalid")
	}
	journal, err := h.Store.GetInvocation(id, invocationID, owner)
	if err != nil {
		return current, err
	}
	if journal["reconciled"] == true {
		if journal["arguments_sha256"] != argsSHA {
			return current, hostError("reconciliation_arguments_changed")
		}
		return current, nil
	}
	run, err := h.Store.Claim(id, owner, h.Settings.LeaseSeconds)
	if err != nil {
		return run, err
	}
	defer h.Store.Release(id, owner, run.LeaseToken)
	// A lost HTTP acknowledgement can be retried after the original run has
	// resumed and removed this invocation from Pending.
	journal, err = h.Store.GetInvocation(id, invocationID, owner)
	if err != nil {
		return run, err
	}
	if journal["reconciled"] == true {
		if journal["arguments_sha256"] != argsSHA {
			return run, hostError("reconciliation_arguments_changed")
		}
		return run, nil
	}
	if run.Revision != revision || current.Revision != revision {
		return run, hostError("revision_conflict")
	}
	state, err := h.restore(run)
	if err != nil {
		return run, err
	}
	if state.Status != "needs_reconciliation" || run.CancelRequested {
		return run, hostError("reconciliation_not_required")
	}
	var item *Invocation
	for i := range state.Pending {
		if state.Pending[i].InvocationID == invocationID {
			item = &state.Pending[i]
			break
		}
	}
	if item == nil || (item.Status != "unknown" && item.Status != "in_flight") {
		return run, hostError("reconciliation_invocation_mismatch")
	}
	if item.ArgumentsSHA256 != argsSHA || ArgumentsDigest(item.Call) != argsSHA {
		return run, hostError("reconciliation_arguments_changed")
	}
	if item.Operation != nil && item.Operation.Binding.PollCapability == "ui.command_status" {
		return run, hostError("reconciliation_use_browser_endpoint")
	}
	if h.Reconciler == nil {
		return run, hostError("reconciliation_unavailable")
	}
	provider, err := h.openRunProvider(ctx, run)
	if err != nil {
		return run, err
	}
	defer provider.Close()
	if previous, ok := run.State["pack_fingerprint"].(string); ok && previous != fingerprint(provider) {
		return run, hostError("pack_changed")
	}
	cap, ok := provider.Capabilities()[item.Call.Capability]
	if !ok {
		return run, hostError("capability_unknown")
	}
	policy, err := h.projectPolicy(ctx, run)
	if err != nil {
		return run, err
	}
	if !policy.GrantedCapabilities[cap.Name] {
		return run, hostError("capability_not_granted")
	}
	inv := invocationContext(run, item, NewID())
	identifyInvocation(&inv, run, provider, cap.Name, policy)
	// Give the verifier an isolated copy: it cannot change the bound arguments
	// or invocation status through the callback context.
	raw, _ := CanonicalJSON(ReconciliationContext{RunID: id, OwnerID: owner, OriginPackID: run.PackID, Invocation: *item, Identity: inv, Capability: cap})
	var verification ReconciliationContext
	if err = strictUnmarshal(raw, &verification); err != nil {
		return run, hostError("run_state_invalid")
	}
	verificationCtx, cancel := context.WithTimeout(ctx, time.Duration(min(h.Settings.InvocationTimeoutSeconds, h.Settings.LeaseSeconds/2)*1e9))
	result, err := h.Reconciler(verificationCtx, verification)
	expired := verificationCtx.Err() != nil
	cancel()
	if ctx.Err() != nil {
		return run, ctx.Err()
	}
	if err != nil || expired {
		return run, hostError("reconciliation_unavailable")
	}
	if err = validateReconciledResult(result, cap, item); err != nil {
		return run, err
	}
	// Authorization may have changed during external verification.
	policy, err = h.projectPolicy(ctx, run)
	if err != nil {
		return run, err
	}
	if !policy.GrantedCapabilities[cap.Name] {
		return run, hostError("capability_not_granted")
	}
	outcome, err := ExecuteCall(ctx, verifiedResultProvider{CapabilityProvider: provider, result: result}, policy.GrantedCapabilities, item.Call, &inv)
	if err != nil {
		return run, err
	}
	if outcome.Fact != nil {
		outcome.Fact.Quality = "verified_reconciliation"
	}
	latest, err := h.Store.GetRun(id, owner)
	if err != nil {
		return run, err
	}
	if latest.LeaseToken != run.LeaseToken {
		return run, ErrLeaseLost
	}
	state.Status, state.ErrorCode = "queued", nil
	for i := range state.Pending {
		other := &state.Pending[i]
		if other != item && (other.Status == "unknown" || other.Status == "in_flight" || other.PollInFlight) {
			state.Status, state.ErrorCode = "needs_reconciliation", strptr("provider_outcome_unknown")
		}
	}
	if latest.CancelRequested {
		state.Status, state.ErrorCode = "cancelled", strptr("cancel_requested")
	}
	item.Reconciled, item.PollInFlight = true, false
	// The reconciled flag, verified Fact, journal and queued state commit together.
	return h.settleInvocation(run, state, item, preparedInvocation{cap: cap, inv: inv, grants: policy.GrantedCapabilities}, outcome)
}

func validateReconciledResult(result CapabilityResult, cap CapabilityDescription, item *Invocation) error {
	if result.ReferenceScope != "" && result.ReferenceScope != "durable" && result.ReferenceScope != "connection" {
		return hostError("reconciliation_result_invalid")
	}
	if validateResult(result) != nil {
		return hostError("reconciliation_result_invalid")
	}
	if _, err := CanonicalJSON(result); err != nil {
		return hostError("reconciliation_result_invalid")
	}
	if result.ErrorCode != "" {
		if !safeCodePattern.MatchString(result.ErrorCode) || unknownOutcome(result.ErrorCode) || definiteAuth(result.ErrorCode) {
			return hostError("reconciliation_result_not_settled")
		}
		return nil
	}
	if cap.OutputSchema != nil {
		schema, err := validateLocalSchema(cap.OutputSchema, false)
		if err != nil || validateSchema(schema, result.Data) != nil {
			return hostError("reconciliation_result_invalid")
		}
	}
	if cap.Operation != nil {
		status, err := operationValue(result.Data, cap.Operation.StatusPath)
		settled, ok := status.(string)
		if err != nil || !ok || (!containsString(cap.Operation.SuccessStates, settled) && !containsString(cap.Operation.FailureStates, settled)) {
			return hostError("reconciliation_result_not_settled")
		}
		if item.Operation != nil {
			id, err := operationValue(result.Data, cap.Operation.IDPath)
			if err != nil || !operationIDMatches(id, item.Operation.OperationID) {
				return hostError("operation_identity_changed")
			}
		}
	}
	return nil
}

func operationIDMatches(value any, expected string) bool {
	id, err := scalarValue(value)
	return err == nil && id == expected
}
