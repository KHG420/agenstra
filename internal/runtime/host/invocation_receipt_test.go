package host

import (
	"context"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func TestCancelledInvocationCanBeVerifiedWithoutResuming(t *testing.T) {
	h, p, run, item := uncertainRun(t)
	if _, err := h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "cancelled" {
		t.Fatal(run.Status, err)
	}
	h.Reconciler = func(_ context.Context, verification agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
		if verification.Invocation.InvocationID != item.InvocationID {
			t.Fatal("verifying another invocation")
		}
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"confirmed": true}}, nil
	}
	run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
	if err != nil || run.Status != "cancelled" {
		t.Fatalf("cancelled operation must remain verifiable: status=%s error=%v", run.Status, err)
	}
	state, err := h.Restore(run)
	if err != nil || !state.Pending[0].Reconciled || len(state.Facts) != 1 {
		t.Fatal(state, err)
	}
	if _, err = h.Drive(t.Context(), run.RunID, "alice"); err != nil || p.calls != 1 {
		t.Fatal("verification resumed or replayed a cancelled write", err, p.calls)
	}
}

func TestCancellationKeepsKnownResponseEvenAfterStorageFailure(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "stored", true: "oversized"}[oversized], func(t *testing.T) {
			cap := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"}}
			p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
			var h *AgentHost
			p.hook = func(ctx context.Context, _ string, _ agentcontract.JSON, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
				if _, err := h.Cancel(ctx, inv.RunID, "alice"); err != nil {
					t.Fatal(err)
				}
				<-ctx.Done()
				data := agentcontract.JSON{"id": "R-1"}
				if oversized {
					data["details"] = strings.Repeat("x", 3000)
				}
				return agentcontract.CapabilityResult{Data: data}, nil
			}
			h = testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision(cap.Name)}})
			h.Settings.LeaseSeconds = 3
			if oversized {
				h.Settings.MaxArtifactBytes = 1024
			}
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "cancelled" {
				t.Fatal("cancellation must not discard a known outcome", run.Status, err)
			}
			state, err := h.Restore(run)
			if err != nil || len(state.InvocationReceipts) != 1 || state.InvocationReceipts[0].Status != "succeeded" {
				t.Fatal(state, err)
			}
			if len(state.Pending) > 0 && state.Pending[0].Status != "succeeded" {
				t.Fatal("known call made unknown again", state.Pending)
			}
		})
	}
}

func TestCancelledAsyncOperationCanBeVerifiedAndIdentityCannotChange(t *testing.T) {
	binding := &agentcontract.OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "job.status", PollArgument: []string{"id"}, IntervalSeconds: 5, TimeoutSeconds: 60, PendingStates: []string{"running"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}}
	cap := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", Effect: "write", Replay: "never", Operation: binding, InputSchema: agentcontract.JSON{"type": "object"}, OutputSchema: agentcontract.JSON{"type": "object"}}
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap, "job.status": {Name: "job.status", Version: "1", Effect: "read", Replay: "safe", InputSchema: agentcontract.JSON{"type": "object"}}}, hook: func(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "job-1", "status": "running"}}, nil
	}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision(cap.Name)}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" {
		t.Fatal(run.Status, err)
	}
	if _, err = h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	state, callErr := h.Restore(run)
	if callErr != nil {
		t.Error(callErr)
	}
	item := state.Pending[0]
	if err != nil || item.Status != "unknown" || item.Receipt.Status != "accepted" || item.Receipt.OperationID != "job-1" {
		t.Fatal(state, err)
	}
	h.Reconciler = func(context.Context, agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "job-2", "status": "succeeded"}}, nil
	}
	if _, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision); agentcontract.ErrorCode(err) != "operation_identity_changed" {
		t.Fatal(err)
	}
	h.Reconciler = func(context.Context, agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "job-1", "status": "succeeded"}}, nil
	}
	run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
	if err != nil || run.Status != "cancelled" || p.calls != 1 {
		t.Fatal(run.Status, err, p.calls)
	}
	var callErr2 error
	state, callErr2 = h.Restore(run)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if state.InvocationReceipts[0].OperationStatus != "succeeded" || !state.InvocationReceipts[0].Reconciled {
		t.Fatal(state.InvocationReceipts)
	}
}

func TestOversizedWritePreservesSuccessfulInvocationReceipt(t *testing.T) {
	for _, limit := range []string{"artifact", "active"} {
		t.Run(limit, func(t *testing.T) {
			cap := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", InputSchema: agentcontract.JSON{"type": "object"}, OutputSchema: agentcontract.JSON{"type": "object"}, Effect: "write", Replay: "never"}
			p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}, hook: func(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
				return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "R-1", "details": strings.Repeat("x", 3000)}}, nil
			}}
			h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision(cap.Name)}})
			if limit == "artifact" {
				h.Settings.MaxArtifactBytes = 1024
			} else {
				h.Settings.MaxActiveArtifactBytes = 1024
			}
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "failed" || p.calls != 1 {
				t.Fatal(run.Status, err, p.calls)
			}
			state, err := h.Restore(run)
			if err != nil || len(state.Facts) != 0 || len(state.Pending) != 1 {
				t.Fatal(state, err)
			}
			journal, err := h.Store.GetInvocation(run.RunID, state.Pending[0].InvocationID, "alice")
			receipt, ok := journal["receipt"].(map[string]any)
			if err != nil || !ok || receipt["status"] != "succeeded" || journal["status"] != "succeeded" {
				t.Fatalf("successful business call lost after storage limit: %v %v", journal, err)
			}
			if digest, _ := receipt["result_sha256"].(string); len(digest) != 64 || receipt["result_error_code"] == nil {
				t.Fatal("missing bounded result evidence", receipt)
			}
			if _, err = h.Drive(t.Context(), run.RunID, "alice"); err != nil || p.calls != 1 {
				t.Fatal("oversized write replayed", err, p.calls)
			}
		})
	}
}

func asyncReceiptHost(t *testing.T, poll func(agentcontract.JSON) agentcontract.JSON, projection *agentcontract.ModelOutput) (*AgentHost, *hostProvider, *float64) {
	t.Helper()
	binding := &agentcontract.OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "job.status", PollArgument: []string{"id"}, IntervalSeconds: 1, TimeoutSeconds: 60, PendingStates: []string{"running"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}}
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{
		"job.submit": {Name: "job.submit", Version: "1", Effect: "write", Replay: "never", ReferenceScope: "durable", InputSchema: agentcontract.JSON{"type": "object"}, Operation: binding, ModelOutput: projection},
		"job.status": {Name: "job.status", Version: "1", Effect: "read", Replay: "safe", ReferenceScope: "durable", InputSchema: agentcontract.JSON{"type": "object"}, ModelOutput: projection},
	}}
	p.hook = func(_ context.Context, name string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		data := agentcontract.JSON{"id": "private-job-token", "status": "running"}
		if name == "job.status" {
			data = poll(args)
		}
		return agentcontract.CapabilityResult{Data: data}, nil
	}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision("job.submit")}})
	now := runstore.UnixNow()
	h.Clock = func() float64 { return now }
	return h, p, &now
}

func TestExpiredOperationChecksPersistedStatusBeforeReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name, status, id, wantRun, wantItem string
	}{
		{"succeeded", "succeeded", "private-job-token", "completed", "succeeded"},
		{"failed", "failed", "private-job-token", "completed", "failed"},
		{"pending", "running", "private-job-token", "needs_reconciliation", "unknown"},
		{"different operation", "succeeded", "other-job", "needs_reconciliation", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, p, now := asyncReceiptHost(t, func(agentcontract.JSON) agentcontract.JSON {
				return agentcontract.JSON{"id": tc.id, "status": tc.status}
			}, nil)
			p.caps["job.submit"].Operation.ReconcileOnTimeout = true
			h.Settings.MaxRunSeconds = 300
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "waiting" {
				t.Fatal(run.Status, err)
			}

			// A new host must settle the persisted operation without replaying submit.
			restarted := NewAgentHost(h.Store, h.ProviderFactory, h.Model, h.PolicyResolver)
			restarted.Settings, restarted.Clock = h.Settings, h.Clock
			h = restarted
			*now += 61
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != tc.wantRun {
				t.Fatal(run.Status, err)
			}
			state, err := h.Restore(run)
			if err != nil {
				t.Fatal(err)
			}
			journal, err := h.Store.GetInvocation(run.RunID, state.InvocationReceipts[0].InvocationID, "alice")
			if err != nil || journal["status"] != tc.wantItem {
				t.Fatal(journal, err)
			}
			if p.calls != 2 {
				t.Fatal("unexpected submit or poll count", p.calls)
			}
			if _, err = h.Drive(t.Context(), run.RunID, "alice"); err != nil || p.calls != 2 {
				t.Fatal("replayed submitted operation", err, p.calls)
			}
		})
	}
}

func TestExpiredOperationKeepsUnknownWhenPollUnavailable(t *testing.T) {
	for _, reason := range []string{"budget", "authorization"} {
		t.Run(reason, func(t *testing.T) {
			h, p, now := asyncReceiptHost(t, func(agentcontract.JSON) agentcontract.JSON {
				return agentcontract.JSON{"id": "private-job-token", "status": "running"}
			}, nil)
			p.caps["job.submit"].Operation.ReconcileOnTimeout = true
			h.Settings.MaxRunSeconds = 300
			h.Settings.MaxPollCalls = 1
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "waiting" {
				t.Fatal(run.Status, err)
			}
			if reason == "budget" {
				*now += 2
				run, err = h.Drive(t.Context(), run.RunID, "alice")
				if err != nil || run.Status != "waiting" || p.calls != 2 {
					t.Fatal(run.Status, err, p.calls)
				}
			} else {
				h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
					return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{"job.submit": true}, AllowModelData: true}, nil
				}
			}
			*now += 61
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if reason == "budget" && err != nil || reason == "authorization" && agentcontract.ErrorCode(err) != "capability_not_granted" {
				t.Fatal(err)
			}
			want := "needs_reconciliation"
			if reason == "authorization" {
				want = "needs_authorization"
			}
			state, err := h.Restore(run)
			if err != nil || run.Status != want || state.Pending[0].Status == "failed" {
				t.Fatal(run.Status, state, err)
			}
			wantCalls := 2
			if reason == "authorization" {
				wantCalls = 1
			}
			if p.calls != wantCalls {
				t.Fatal("unsafe or over-budget poll", p.calls)
			}
		})
	}
}

func TestExpiredOperationResumesPersistedPollIdentity(t *testing.T) {
	h, p, now := asyncReceiptHost(t, func(agentcontract.JSON) agentcontract.JSON {
		return agentcontract.JSON{"id": "private-job-token", "status": "succeeded"}
	}, nil)
	p.caps["job.submit"].Operation.ReconcileOnTimeout = true
	h.Settings.MaxRunSeconds, h.Settings.MaxPollCalls = 300, 1
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" {
		t.Fatal(run.Status, err)
	}
	run, err = h.Store.Claim(run.RunID, "alice", h.Settings.LeaseSeconds)
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	item := &state.Pending[0]
	pollID := item.InvocationID + ":poll:1"
	item.PollInFlight = true
	item.Operation.Polls = 1
	state.PollCallsUsed = 1
	run, err = h.save(run, state, "", run.NextWakeAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Store.Release(run.RunID, "alice", run.LeaseToken); err != nil {
		t.Fatal(err)
	}
	p.hook = func(_ context.Context, name string, _ agentcontract.JSON, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		if name != "job.status" || inv.InvocationID != pollID || inv.IdempotencyKey != pollID {
			t.Fatal("poll identity changed", name, inv.InvocationID, inv.IdempotencyKey)
		}
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "private-job-token", "status": "succeeded"}}, nil
	}
	*now += 61
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 2 {
		t.Fatal(run.Status, err, p.calls)
	}
	state, err = h.Restore(run)
	if err != nil || state.PollCallsUsed != 1 {
		t.Fatal(state, err)
	}
}

func TestAcceptedOperationPollBudgetExhaustionNeedsReconciliation(t *testing.T) {
	h, p, now := asyncReceiptHost(t, func(agentcontract.JSON) agentcontract.JSON {
		return agentcontract.JSON{"id": "private-job-token", "status": "running"}
	}, nil)
	h.Settings.MaxRunSeconds, h.Settings.MaxPollCalls = 300, 1
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" {
		t.Fatal(run.Status, err)
	}
	*now += 2
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" || p.calls != 2 {
		t.Fatal(run.Status, err, p.calls)
	}
	*now += 2 // Still before the 60-second operation deadline.
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_reconciliation" {
		t.Fatal(run.Status, err)
	}
	state, err := h.Restore(run)
	if err != nil || state.Pending[0].Status != "unknown" || state.Pending[0].Receipt.Status != "accepted" || p.calls != 2 {
		t.Fatal(state, err, p.calls)
	}
}

func TestPollValidatesOperationBeforeReplacingEvidence(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		for _, invalid := range []string{"identity", "status"} {
			t.Run(invalid+map[bool]string{false: "/normal", true: "/oversized"}[oversized], func(t *testing.T) {
				h, _, now := asyncReceiptHost(t, func(agentcontract.JSON) agentcontract.JSON {
					data := agentcontract.JSON{"id": "private-job-token", "status": "succeeded"}
					if invalid == "identity" {
						data["id"] = "other-job"
					} else {
						data["status"] = "not-in-contract"
					}
					if oversized {
						data["details"] = strings.Repeat("x", 3000)
					}
					return data
				}, nil)
				h.Settings.MaxArtifactBytes = 1024
				run := createTestHostRun(t, h)
				run, err := h.Drive(t.Context(), run.RunID, "alice")
				if err != nil || run.Status != "waiting" {
					t.Fatal(run.Status, err)
				}
				before, callErr3 := h.Restore(run)
				if callErr3 != nil {
					t.Error(callErr3)
				}
				original := before.Pending[0]
				*now += 2
				run, err = h.Drive(t.Context(), run.RunID, "alice")
				state, callErr4 := h.Restore(run)
				if callErr4 != nil {
					t.Error(callErr4)
				}
				item := state.Pending[0]
				if err != nil || run.Status != "needs_reconciliation" || item.Status != "unknown" {
					t.Fatal(run.Status, item, err)
				}
				if *item.FactID != *original.FactID || item.Operation.OperationID != "private-job-token" || item.Receipt.Status != "accepted" || item.Receipt.ResultSHA256 != original.Receipt.ResultSHA256 || len(state.Facts) != 1 || state.Facts[0].FactID != before.Facts[0].FactID {
					t.Fatal("invalid poll replaced original evidence", state)
				}
			})
		}
	}
}

func TestTerminalAsyncRunCanBeVerifiedAfterBudgetOrDeadline(t *testing.T) {
	for _, stop := range []string{"deadline", "poll_budget"} {
		t.Run(stop, func(t *testing.T) {
			h, p, now := asyncReceiptHost(t, func(agentcontract.JSON) agentcontract.JSON {
				return agentcontract.JSON{"id": "private-job-token", "status": "running"}
			}, nil)
			h.Settings.MaxRunSeconds, h.Settings.MaxPollCalls = 10, 1
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "waiting" {
				t.Fatal(run.Status, err)
			}
			if stop == "deadline" {
				*now += 11
			} else {
				*now += 2
				run, err = h.Drive(t.Context(), run.RunID, "alice")
				if err != nil || run.Status != "waiting" {
					t.Fatal(run.Status, err)
				}
				*now += 2
			}
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			wantStatus := "failed"
			if stop == "poll_budget" {
				wantStatus = "needs_reconciliation"
			}
			if err != nil || run.Status != wantStatus {
				t.Fatal(run.Status, err)
			}
			state, callErr5 := h.Restore(run)
			if callErr5 != nil {
				t.Error(callErr5)
			}
			item, calls := state.Pending[0], p.calls
			h.Reconciler = func(context.Context, agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
				return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "private-job-token", "status": "succeeded"}}, nil
			}
			run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
			if stop == "poll_budget" {
				wantStatus = "queued"
			}
			if err != nil || run.Status != wantStatus || p.calls != calls {
				t.Fatal("reconciliation changed execution unexpectedly", run.Status, err, p.calls)
			}
			var callErr6 error
			state, callErr6 = h.Restore(run)
			if callErr6 != nil {
				t.Error(callErr6)
			}
			if state.InvocationReceipts[0].Status != "succeeded" || !state.InvocationReceipts[0].Reconciled {
				t.Fatal(state.InvocationReceipts)
			}
			journal, err := h.Store.GetInvocation(run.RunID, item.InvocationID, "alice")
			if err != nil || journal["error_code"] != nil {
				t.Fatal("verified operation retained the previous uncertainty error", journal["error_code"], err)
			}
			resumed, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || p.calls != calls || stop == "deadline" && resumed.Status != "failed" || stop == "poll_budget" && resumed.Status != "completed" {
				t.Fatal("verified task replayed its operation", resumed.Status, err, p.calls)
			}
		})
	}
}

func TestAutomaticPollArgumentsRespectModelProjection(t *testing.T) {
	h, _, now := asyncReceiptHost(t, func(args agentcontract.JSON) agentcontract.JSON {
		if args["id"] != "private-job-token" {
			t.Fatal("execution lost its operation binding", args)
		}
		return agentcontract.JSON{"id": "private-job-token", "status": "succeeded"}
	}, &agentcontract.ModelOutput{Paths: [][]string{{"status"}}})
	h.Model.(*hostModel).hook = func(packet agentcontract.ContextPacket) {
		raw, err := agentcontract.CanonicalJSON(packet)
		if err != nil || strings.Contains(string(raw), "private-job-token") {
			t.Fatal("internal operation token entered model context", string(raw), err)
		}
	}
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" {
		t.Fatal(run.Status, err)
	}
	*now += 2
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatal(run.Status, err)
	}
	state, callErr7 := h.Restore(run)
	if callErr7 != nil {
		t.Error(callErr7)
	}
	lastAudit := state.Observations[len(state.Observations)-1]
	lastModel := state.ModelObservations[len(state.ModelObservations)-1]
	if lastAudit.Arguments["id"] != "private-job-token" || !lastModel.ArgumentsOmitted || len(lastModel.Arguments) != 0 {
		t.Fatal("audit/model observation separation lost", lastAudit, lastModel)
	}
	artifact, err := h.Store.GetArtifact(run.RunID, state.Facts[0].FactID, "alice")
	raw, callErr8 := agentcontract.CanonicalJSON(artifact)
	if callErr8 != nil {
		t.Error(callErr8)
	}
	if err != nil || !strings.Contains(string(raw), "private-job-token") {
		t.Fatal("full business artifact was not retained", err)
	}
}
