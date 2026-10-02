package agenstra

import (
	"context"
	"strings"
	"testing"
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
	h.Reconciler = func(_ context.Context, verification ReconciliationContext) (CapabilityResult, error) {
		if verification.Invocation.InvocationID != item.InvocationID {
			t.Fatal("verifying another invocation")
		}
		return CapabilityResult{Data: JSON{"confirmed": true}}, nil
	}
	run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
	if err != nil || run.Status != "cancelled" {
		t.Fatalf("cancelled operation must remain verifiable: status=%s error=%v", run.Status, err)
	}
	state, err := h.restore(run)
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
			cap := CapabilityDescription{Name: "records.get", Version: "1", Effect: "write", Replay: "never", InputSchema: JSON{"type": "object"}}
			p := &hostProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}
			var h *AgentHost
			p.hook = func(ctx context.Context, _ string, _ JSON, inv *InvocationContext) (CapabilityResult, error) {
				if _, err := h.Cancel(ctx, inv.RunID, "alice"); err != nil {
					t.Fatal(err)
				}
				<-ctx.Done()
				data := JSON{"id": "R-1"}
				if oversized {
					data["details"] = strings.Repeat("x", 3000)
				}
				return CapabilityResult{Data: data}, nil
			}
			h = testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision(cap.Name)}})
			h.Settings.LeaseSeconds = 3
			if oversized {
				h.Settings.MaxArtifactBytes = 1024
			}
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "cancelled" {
				t.Fatal("cancellation must not discard a known outcome", run.Status, err)
			}
			state, err := h.restore(run)
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
	binding := &OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "job.status", PollArgument: []string{"id"}, IntervalSeconds: 5, TimeoutSeconds: 60, PendingStates: []string{"running"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}}
	cap := CapabilityDescription{Name: "records.get", Version: "1", Effect: "write", Replay: "never", Operation: binding, InputSchema: JSON{"type": "object"}, OutputSchema: JSON{"type": "object"}}
	p := &hostProvider{caps: map[string]CapabilityDescription{cap.Name: cap, "job.status": {Name: "job.status", Version: "1", Effect: "read", Replay: "safe", InputSchema: JSON{"type": "object"}}}, hook: func(context.Context, string, JSON, *InvocationContext) (CapabilityResult, error) {
		return CapabilityResult{Data: JSON{"id": "job-1", "status": "running"}}, nil
	}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision(cap.Name)}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" {
		t.Fatal(run.Status, err)
	}
	if _, err = h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	state, _ := h.restore(run)
	item := state.Pending[0]
	if err != nil || item.Status != "unknown" || item.Receipt.Status != "accepted" || item.Receipt.OperationID != "job-1" {
		t.Fatal(state, err)
	}
	h.Reconciler = func(context.Context, ReconciliationContext) (CapabilityResult, error) {
		return CapabilityResult{Data: JSON{"id": "job-2", "status": "succeeded"}}, nil
	}
	if _, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision); ErrorCode(err) != "operation_identity_changed" {
		t.Fatal(err)
	}
	h.Reconciler = func(context.Context, ReconciliationContext) (CapabilityResult, error) {
		return CapabilityResult{Data: JSON{"id": "job-1", "status": "succeeded"}}, nil
	}
	run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
	if err != nil || run.Status != "cancelled" || p.calls != 1 {
		t.Fatal(run.Status, err, p.calls)
	}
	state, _ = h.restore(run)
	if state.InvocationReceipts[0].OperationStatus != "succeeded" || !state.InvocationReceipts[0].Reconciled {
		t.Fatal(state.InvocationReceipts)
	}
}

func TestOversizedWritePreservesSuccessfulInvocationReceipt(t *testing.T) {
	for _, limit := range []string{"artifact", "active"} {
		t.Run(limit, func(t *testing.T) {
			cap := CapabilityDescription{Name: "records.get", Version: "1", InputSchema: JSON{"type": "object"}, OutputSchema: JSON{"type": "object"}, Effect: "write", Replay: "never"}
			p := &hostProvider{caps: map[string]CapabilityDescription{cap.Name: cap}, hook: func(context.Context, string, JSON, *InvocationContext) (CapabilityResult, error) {
				return CapabilityResult{Data: JSON{"id": "R-1", "details": strings.Repeat("x", 3000)}}, nil
			}}
			h := testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision(cap.Name)}})
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
			state, err := h.restore(run)
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

func asyncReceiptHost(t *testing.T, poll func(JSON) JSON, projection *ModelOutput) (*AgentHost, *hostProvider, *float64) {
	t.Helper()
	binding := &OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "job.status", PollArgument: []string{"id"}, IntervalSeconds: 1, TimeoutSeconds: 60, PendingStates: []string{"running"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}}
	p := &hostProvider{caps: map[string]CapabilityDescription{
		"job.submit": {Name: "job.submit", Version: "1", Effect: "write", Replay: "never", ReferenceScope: "durable", InputSchema: JSON{"type": "object"}, Operation: binding, ModelOutput: projection},
		"job.status": {Name: "job.status", Version: "1", Effect: "read", Replay: "safe", ReferenceScope: "durable", InputSchema: JSON{"type": "object"}, ModelOutput: projection},
	}}
	p.hook = func(_ context.Context, name string, args JSON, _ *InvocationContext) (CapabilityResult, error) {
		data := JSON{"id": "private-job-token", "status": "running"}
		if name == "job.status" {
			data = poll(args)
		}
		return CapabilityResult{Data: data}, nil
	}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision("job.submit")}})
	now := unixNow()
	h.Clock = func() float64 { return now }
	return h, p, &now
}

func TestPollValidatesOperationBeforeReplacingEvidence(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		for _, invalid := range []string{"identity", "status"} {
			t.Run(invalid+map[bool]string{false: "/normal", true: "/oversized"}[oversized], func(t *testing.T) {
				h, _, now := asyncReceiptHost(t, func(JSON) JSON {
					data := JSON{"id": "private-job-token", "status": "succeeded"}
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
				before, _ := h.restore(run)
				original := before.Pending[0]
				*now += 2
				run, err = h.Drive(t.Context(), run.RunID, "alice")
				state, _ := h.restore(run)
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
			h, p, now := asyncReceiptHost(t, func(JSON) JSON {
				return JSON{"id": "private-job-token", "status": "running"}
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
			if err != nil || run.Status != "failed" {
				t.Fatal(run.Status, err)
			}
			state, _ := h.restore(run)
			item, calls := state.Pending[0], p.calls
			h.Reconciler = func(context.Context, ReconciliationContext) (CapabilityResult, error) {
				return CapabilityResult{Data: JSON{"id": "private-job-token", "status": "succeeded"}}, nil
			}
			run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
			if err != nil || run.Status != "failed" || p.calls != calls {
				t.Fatal("terminal run must be verified without resuming", run.Status, err, p.calls)
			}
			state, _ = h.restore(run)
			if state.InvocationReceipts[0].Status != "succeeded" || !state.InvocationReceipts[0].Reconciled {
				t.Fatal(state.InvocationReceipts)
			}
			if _, err = h.Drive(t.Context(), run.RunID, "alice"); err != nil || p.calls != calls {
				t.Fatal("stopped task resumed", err)
			}
		})
	}
}

func TestAutomaticPollArgumentsRespectModelProjection(t *testing.T) {
	h, _, now := asyncReceiptHost(t, func(args JSON) JSON {
		if args["id"] != "private-job-token" {
			t.Fatal("execution lost its operation binding", args)
		}
		return JSON{"id": "private-job-token", "status": "succeeded"}
	}, &ModelOutput{Paths: [][]string{{"status"}}})
	h.Model.(*hostModel).hook = func(packet ContextPacket) {
		raw, err := CanonicalJSON(packet)
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
	state, _ := h.restore(run)
	lastAudit := state.Observations[len(state.Observations)-1]
	lastModel := state.ModelObservations[len(state.ModelObservations)-1]
	if lastAudit.Arguments["id"] != "private-job-token" || !lastModel.ArgumentsOmitted || len(lastModel.Arguments) != 0 {
		t.Fatal("audit/model observation separation lost", lastAudit, lastModel)
	}
	artifact, err := h.Store.GetArtifact(run.RunID, state.Facts[0].FactID, "alice")
	raw, _ := CanonicalJSON(artifact)
	if err != nil || !strings.Contains(string(raw), "private-job-token") {
		t.Fatal("full business artifact was not retained", err)
	}
}
