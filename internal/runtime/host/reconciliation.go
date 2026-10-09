package host

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

type verifiedResultProvider struct {
	agentcontract.CapabilityProvider
	result agentcontract.CapabilityResult
}

// Invoke returns the verified result without repeating the original external operation.
func (p verifiedResultProvider) Invoke(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
	return p.result, nil
}

// Reconcile rechecks identity, authorization, revision and arguments before verifying an uncertain outcome.
// It retains the original invocation and never accepts client-authored success evidence.
func (h *AgentHost) Reconcile(ctx context.Context, id, owner, invocationID, argsSHA string, revision int) (agentcontract.StoredRun, error) {
	return h.VerifyInvocation(ctx, id, owner, invocationID, argsSHA, revision, h.Reconciler, false)
}

// VerifyInvocation settles original uncertain evidence through a trusted verifier without replaying the operation. Authorization, revision and original identity are rechecked.
func (h *AgentHost) VerifyInvocation(ctx context.Context, id, owner, invocationID, argsSHA string, revision int, verifier agentcontract.InvocationReconciler, browser bool) (agentcontract.StoredRun, error) {
	h.InitializeDefaults()
	current, err := h.Get(ctx, id, owner)
	if err != nil {
		return current, err
	}
	if !agentcontract.ValidUUID(invocationID) || len(argsSHA) != 64 || strings.Trim(argsSHA, "0123456789abcdef") != "" || revision < 0 {
		return current, agentcontract.NewHostError("reconciliation_invalid")
	}
	journal, err := h.Store.GetInvocation(id, invocationID, owner)
	if err != nil {
		return current, err
	}
	if journal["reconciled"] == true {
		if journal["arguments_sha256"] != argsSHA {
			return current, agentcontract.NewHostError("reconciliation_arguments_changed")
		}
		return current, nil
	}
	run, err := h.Store.Claim(id, owner, h.Settings.LeaseSeconds)
	if err != nil {
		return run, err
	}
	defer func() {

		// The lease expires if release fails; keep the committed run outcome.
		if err := h.Store.Release(id, owner, run.LeaseToken); err != nil {
			log.Print("run lease release failed")
		}
	}()

	// A lost HTTP acknowledgement can be retried after the original run has
	// resumed and removed this invocation from Pending.
	journal, err = h.Store.GetInvocation(id, invocationID, owner)
	if err != nil {
		return run, err
	}
	if journal["reconciled"] == true {
		if journal["arguments_sha256"] != argsSHA {
			return run, agentcontract.NewHostError("reconciliation_arguments_changed")
		}
		return run, nil
	}
	if run.Revision != revision || current.Revision != revision {
		return run, agentcontract.NewHostError("revision_conflict")
	}
	state, err := h.Restore(run)
	if err != nil {
		return run, err
	}
	stopped := state.Status == "cancelled" || state.Status == "failed"
	if state.Status != "needs_reconciliation" && !stopped {
		return run, agentcontract.NewHostError("reconciliation_not_required")
	}
	var item *agentcontract.Invocation
	for i := range state.Pending {
		if state.Pending[i].InvocationID == invocationID {
			item = &state.Pending[i]
			break
		}
	}
	if item == nil || !unsettledInvocation(*item) {
		return run, agentcontract.NewHostError("reconciliation_invocation_mismatch")
	}
	if item.ArgumentsSHA256 != argsSHA || reactcore.ArgumentsDigest(item.Call) != argsSHA {
		return run, agentcontract.NewHostError("reconciliation_arguments_changed")
	}
	if item.Operation != nil && item.Operation.Binding.PollCapability == "ui.command_status" && !browser {
		return run, agentcontract.NewHostError("reconciliation_use_browser_endpoint")
	}
	if verifier == nil {
		return run, agentcontract.NewHostError("reconciliation_unavailable")
	}
	provider, err := h.OpenRunProvider(ctx, run)
	if err != nil {
		return run, err
	}
	defer func() {

		// Closing the connection does not change an already observed business outcome.
		if err := provider.Close(); err != nil {
			log.Print("provider cleanup failed")
		}
	}()
	fp := Fingerprint(provider)
	if fp == "" {
		return run, agentcontract.NewHostError("pack_changed")
	}
	if previous, ok := run.State["pack_fingerprint"].(string); ok && previous != fp {
		return run, agentcontract.NewHostError("pack_changed")
	}
	cap, ok := provider.Capabilities()[item.Call.Capability]
	if !ok {
		return run, agentcontract.NewHostError("capability_unknown")
	}
	policy, err := h.ProjectPolicy(ctx, run)
	if err != nil {
		return run, err
	}
	if !policy.GrantedCapabilities[cap.Name] {
		return run, agentcontract.NewHostError("capability_not_granted")
	}
	inv := invocationContext(run, item, agentcontract.NewID())
	IdentifyInvocation(&inv, run, provider, cap.Name, policy)

	// Give the verifier an isolated copy: it cannot change the bound arguments
	// or invocation status through the callback context.
	raw, err := agentcontract.CanonicalJSON(agentcontract.ReconciliationContext{RunID: id, OwnerID: owner, OriginPackID: run.PackID, Invocation: *item, Identity: inv, Capability: cap})
	if err != nil {
		return run, agentcontract.NewHostError("run_state_invalid")
	}
	var verification agentcontract.ReconciliationContext
	if err = jsonvalue.DecodeStrict(raw, &verification); err != nil {
		return run, agentcontract.NewHostError("run_state_invalid")
	}
	verificationCtx, cancel := context.WithTimeout(ctx, time.Duration(min(h.runSettings(run).InvocationTimeoutSeconds, h.Settings.LeaseSeconds/2)*1e9))
	result, err := verifier(verificationCtx, verification)
	expired := verificationCtx.Err() != nil
	cancel()
	if ctx.Err() != nil {
		return run, ctx.Err()
	}
	if err != nil || expired {
		return run, agentcontract.NewHostError("reconciliation_unavailable")
	}
	if err = validateReconciledResult(result, cap, item); err != nil {
		return run, err
	}

	// Authorization may have changed during external verification.
	policy, err = h.ProjectPolicy(ctx, run)
	if err != nil {
		return run, err
	}
	if !policy.GrantedCapabilities[cap.Name] {
		return run, agentcontract.NewHostError("capability_not_granted")
	}
	call, evidenceIdentity := item.Call, inv
	if browser {

		// Browser completion evidence keeps the same ui.command_status source
		// as normal polling, while settlement remains bound to the original action.
		if item.Operation == nil || pollAccess(provider, policy, item.Operation.Binding) != nil {
			return run, agentcontract.NewHostError("reconciliation_binding_mismatch")
		}
		call = agentcontract.ToolCall{CallRef: item.Call.CallRef, Capability: item.Operation.Binding.PollCapability, Arguments: item.Operation.PollArguments, Reason: "Read verified browser receipt"}
		IdentifyInvocation(&evidenceIdentity, run, provider, call.Capability, policy)
	}
	outcome, err := reactcore.ExecuteCall(ctx, verifiedResultProvider{CapabilityProvider: provider, result: result}, policy.GrantedCapabilities, call, &evidenceIdentity)
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
		return run, runstore.ErrLeaseLost
	}
	if !stopped {
		state.Status, state.ErrorCode = "queued", nil
	}
	for i := range state.Pending {
		other := &state.Pending[i]
		if !stopped && other != item && (other.Status == "unknown" || other.Status == "in_flight" || other.PollInFlight) {
			state.Status, state.ErrorCode = "needs_reconciliation", agentcontract.Strptr("provider_outcome_unknown")
		}
	}
	if latest.CancelRequested {
		state.Status, state.ErrorCode = "cancelled", agentcontract.Strptr("cancel_requested")
	}
	item.Reconciled, item.PollInFlight = true, false

	// The reconciled flag, verified Fact, journal and queued state commit together.
	return h.settleInvocation(run, state, item, preparedInvocation{cap: cap, inv: inv, grants: policy.GrantedCapabilities}, outcome)
}

func validateReconciledResult(result agentcontract.CapabilityResult, cap agentcontract.CapabilityDescription, item *agentcontract.Invocation) error {
	if result.ReferenceScope != "" && result.ReferenceScope != "durable" && result.ReferenceScope != "connection" {
		return agentcontract.NewHostError("reconciliation_result_invalid")
	}
	if validateResult(result) != nil {
		return agentcontract.NewHostError("reconciliation_result_invalid")
	}
	if _, err := agentcontract.CanonicalJSON(result); err != nil {
		return agentcontract.NewHostError("reconciliation_result_invalid")
	}
	if result.ErrorCode != "" {
		if !agentcontract.SafeCodePattern.MatchString(result.ErrorCode) || reactcore.UnknownOutcome(result.ErrorCode) || definiteAuth(result.ErrorCode) {
			return agentcontract.NewHostError("reconciliation_result_not_settled")
		}
		return nil
	}
	if cap.OutputSchema != nil {
		schema, err := agentcontract.ValidateLocalSchema(cap.OutputSchema, false)
		if err != nil || agentcontract.ValidateSchema(schema, result.Data) != nil {
			return agentcontract.NewHostError("reconciliation_result_invalid")
		}
	}
	if cap.Operation != nil {
		status, err := reactcore.OperationValue(result.Data, cap.Operation.StatusPath)
		settled, ok := status.(string)
		if err != nil || !ok || (!agentcontract.ContainsString(cap.Operation.SuccessStates, settled) && !agentcontract.ContainsString(cap.Operation.FailureStates, settled)) {
			return agentcontract.NewHostError("reconciliation_result_not_settled")
		}
		expectedID := ""
		if item.Operation != nil {
			expectedID = item.Operation.OperationID
		} else if item.Receipt != nil {
			expectedID = item.Receipt.OperationID
		}
		if expectedID != "" {
			id, err := reactcore.OperationValue(result.Data, cap.Operation.IDPath)
			if err != nil || !operationIDMatches(id, expectedID) {
				return agentcontract.NewHostError("operation_identity_changed")
			}
		}
	}
	return nil
}

func operationIDMatches(value any, expected string) bool {
	id, err := agentcontract.ScalarValue(value)
	return err == nil && id == expected
}
