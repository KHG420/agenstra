package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
)

func TestDeploymentCompletionChecksApplyOnlyToUsedOperations(t *testing.T) {
	h := &durablehost.AgentHost{}
	d := &Deployment{Config: agentcontract.DeploymentConfig{CompletionChecks: map[string][]agentcontract.FactRequirement{"orders": {{Capability: "orders.approve", Path: []any{"approved"}, Value: true}}}}}
	if err := ConfigureDeploymentChecks(h, d); err != nil {
		t.Fatal(err)
	}
	if err := h.CompletionValidator(t.Context(), agentcontract.CompletionContext{OriginPackID: "orders"}); err != nil {
		t.Fatal("greeting required a write", err)
	}
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "orders.approve", Value: agentcontract.JSON{"approved": false}}
	result := agentcontract.CompletionContext{OriginPackID: "orders", FactIDs: []string{fact.FactID}, Facts: []agentcontract.Fact{fact}}
	if err := h.CompletionValidator(t.Context(), result); agentcontract.ErrorCode(err) != "completion_evidence_mismatch" {
		t.Fatal(err)
	}
	fact.Value["approved"] = true
	result.Facts = []agentcontract.Fact{fact}
	if err := h.CompletionValidator(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	result.Observations = []agentcontract.Observation{{Capability: "orders.approve", ErrorCode: agentcontract.Strptr("operation_failed")}}
	if err := h.CompletionValidator(t.Context(), result); agentcontract.ErrorCode(err) != "completion_operation_failed" {
		t.Fatal(err)
	}
	result.Facts = nil
	if err := h.CompletionValidator(t.Context(), result); err == nil {
		t.Fatal("failed operation passed with no evidence")
	}
}

func TestDeploymentCompletionRequiredOperationRejectsFinalWithoutAction(t *testing.T) {
	provider := &hostProvider{}
	h := testHost(t, testStore(t), provider, &hostModel{})
	d := &Deployment{Config: agentcontract.DeploymentConfig{CompletionChecks: map[string][]agentcontract.FactRequirement{"orders": {{Capability: "orders.approve", Path: []any{"approved"}, Value: true, Required: true}}}}}
	if err := ConfigureDeploymentChecks(h, d); err != nil {
		t.Fatal(err)
	}
	if code := agentcontract.ErrorCode(h.CompletionValidator(t.Context(), agentcontract.CompletionContext{OriginPackID: "orders"})); code != "completion_capability_required" {
		t.Fatal("required operation was skipped", code)
	}
	run, err := h.Create(t.Context(), "alice", "orders", "Approve the order", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status == "completed" || provider.calls != 0 {
		t.Fatal("model completed without required action", run.Status, err, provider.calls)
	}
}

func TestDeploymentCompletionAllowsDeniedApprovalWithoutSkippingOtherEvidence(t *testing.T) {
	for _, required := range []bool{false, true} {
		h := &durablehost.AgentHost{}
		d := &Deployment{Config: agentcontract.DeploymentConfig{CompletionChecks: map[string][]agentcontract.FactRequirement{"records": {
			{Capability: "records.update", Path: []any{"data", "ok"}, Value: true, Required: required},
			{Capability: "records.other", Path: []any{"data", "ok"}, Value: true},
		}}}}
		if err := ConfigureDeploymentChecks(h, d); err != nil {
			t.Fatal(err)
		}
		denied := agentcontract.Observation{CallRef: "denied", Capability: "records.update", Status: "failed", ErrorCode: agentcontract.Strptr("approval_denied")}
		result := agentcontract.CompletionContext{OriginPackID: "records", AnswerMarkdown: "你已拒绝审批，本次没有执行写入。", Observations: []agentcontract.Observation{denied}}
		if err := h.CompletionValidator(t.Context(), result); err != nil {
			t.Fatal("denied approval could not finish", required, err)
		}
		fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "records.update", Value: agentcontract.JSON{"data": agentcontract.JSON{"ok": false}}}
		result.Facts, result.FactIDs = []agentcontract.Fact{fact}, []string{fact.FactID}
		result.Observations = []agentcontract.Observation{{CallRef: "earlier", Capability: fact.SourceCapability, Status: "succeeded", FactID: &fact.FactID}, denied}
		if code := agentcontract.ErrorCode(h.CompletionValidator(t.Context(), result)); code != "completion_evidence_mismatch" {
			t.Fatal("denial bypassed an earlier operation's evidence", code)
		}
		result.Facts, result.FactIDs = nil, nil
		result.Observations[0].Status = "failed"
		result.Observations[0].ErrorCode = agentcontract.Strptr("operation_failed")
		if code := agentcontract.ErrorCode(h.CompletionValidator(t.Context(), result)); code != "completion_operation_failed" {
			t.Fatal("denial hid an earlier failed operation", code)
		}
		result.Observations[0].Capability = "records.other"
		if code := agentcontract.ErrorCode(h.CompletionValidator(t.Context(), result)); code != "completion_operation_failed" {
			t.Fatal("denial bypassed an independent capability check", code)
		}
	}
}

func TestHostFinishesTruthfulAnswerAfterDeniedApprovalWithCompletionCheck(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.get", Effect: "write", Replay: "never", ApprovalRequired: true, InputSchema: agentcontract.JSON{"type": "object"}}
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
	answer := "你已拒绝审批，本次没有执行写入。"
	m := &hostModel{decisions: []agentcontract.Decision{callDecision(cap.Name), {Kind: "final", AnswerMarkdown: answer}, {Kind: "final", AnswerMarkdown: answer}}}
	h := testHost(t, testStore(t), p, m)
	h.Settings.MaxModelRounds = 8
	d := &Deployment{Config: agentcontract.DeploymentConfig{CompletionChecks: map[string][]agentcontract.FactRequirement{"records": {{Capability: cap.Name, Path: []any{"data", "ok"}, Value: true}}}}}
	if err := ConfigureDeploymentChecks(h, d); err != nil {
		t.Fatal(err)
	}
	r := createTestHostRun(t, h)
	r, err := h.Drive(t.Context(), r.RunID, "alice")
	if err != nil || r.Status != "needs_approval" {
		t.Fatal(r.Status, err)
	}
	s, err := h.Restore(r)
	if err != nil {
		t.Fatal(err)
	}
	item := s.Pending[0]
	if _, err = h.Approve(t.Context(), r.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, r.Revision, false); err != nil {
		t.Fatal(err)
	}
	r, err = h.Drive(t.Context(), r.RunID, "alice")
	if err != nil || r.Status != "completed" || p.calls != 0 {
		t.Fatal("denied run", r.Status, err, p.calls)
	}
	s, err = h.Restore(r)
	if err != nil || s.AnswerMarkdown != answer {
		t.Fatal("truthful final not published", err, s)
	}
}

func TestDeclarativeReconciliationChecksOriginalIdentityAndReadPermission(t *testing.T) {
	for _, mode := range []string{"confirmed", "not_confirmed", "not_granted", "write_verifier", "large_number", "large_number_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			cap := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", InputSchema: agentcontract.JSON{"type": "object"}, OutputSchema: agentcontract.JSON{"type": "object", "required": []any{"confirmed"}, "properties": agentcontract.JSON{"confirmed": agentcontract.JSON{"const": true}}}, Effect: "write", Replay: "never"}
			verify := agentcontract.CapabilityDescription{Name: "records.verify", Version: "1", InputSchema: agentcontract.JSON{"type": "object"}, OutputSchema: agentcontract.JSON{"type": "object"}, Effect: "read", Replay: "safe"}
			if mode == "write_verifier" {
				verify.Effect = "write"
			}
			writes, reads := 0, 0
			p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap, verify.Name: verify}, hook: func(_ context.Context, name string, args agentcontract.JSON, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
				if name == cap.Name {
					writes++
					return agentcontract.CapabilityResult{}, errors.New("lost response")
				}
				reads++
				query, _ := args["query"].(map[string]any)
				if query["request_id"] == nil || query["request_id"] == "" {
					t.Error("original identity absent")
				}
				var found any = mode != "not_confirmed"
				if strings.HasPrefix(mode, "large_number") {
					found = json.Number("9007199254740993")
				}
				return agentcontract.CapabilityResult{Data: agentcontract.JSON{"found": found, "result": agentcontract.JSON{"confirmed": true}}}, nil
			}}
			h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision(cap.Name)}})
			h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
				return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{cap.Name: true, verify.Name: mode != "not_granted"}, AllowModelData: true}, nil
			}
			rule := agentcontract.ReconciliationRule{VerifyCapability: verify.Name, Arguments: map[string][]any{"/query/request_id": {"idempotency_key"}}, SuccessPath: []any{"found"}, SuccessValue: true, ResultPath: []any{"result"}}
			if mode == "large_number" {
				rule.SuccessValue = json.Number("9007199254740993")
			} else if mode == "large_number_mismatch" {
				rule.SuccessValue = json.Number("9007199254740992")
			}
			d := &Deployment{Config: agentcontract.DeploymentConfig{ReconciliationChecks: map[string]map[string]agentcontract.ReconciliationRule{"records": {cap.Name: rule}}}}
			if err := ConfigureDeploymentChecks(h, d); err != nil {
				t.Fatal(err)
			}
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "needs_reconciliation" {
				t.Fatal(run.Status, err)
			}
			state, callErr5 := h.Restore(run)
			if callErr5 != nil {
				t.Error(callErr5)
			}
			item := state.Pending[0]
			run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
			if mode == "confirmed" || mode == "large_number" {
				if err != nil || run.Status != "queued" || reads != 1 {
					t.Fatal(run.Status, err, reads)
				}
			} else if err == nil {
				t.Fatal("unverified result accepted")
			}
			if writes != 1 {
				t.Fatal("original write replayed", writes)
			}
		})
	}
	bad := &Deployment{Config: agentcontract.DeploymentConfig{ReconciliationChecks: map[string]map[string]agentcontract.ReconciliationRule{"records": {"records.get": {VerifyCapability: "records.verify"}}}}}
	if err := ConfigureDeploymentChecks(&durablehost.AgentHost{}, bad); err == nil {
		t.Fatal("uncorrelated query accepted")
	}
}
