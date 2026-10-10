package host

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestBrowserContextDiagnosticsIdentifyPagePrerequisite(t *testing.T) {
	for _, code := range []string{"browser_context_required", "browser_context_changed"} {
		finding := ExplainRunError(code, "ui.read_activity")
		if finding.Category != "browser" || !strings.Contains(finding.NextAction, "ui.get_context") {
			t.Fatalf("misdirected page prerequisite: %+v", finding)
		}
	}
	if finding := ExplainRunError("browser_handler_outcome_unknown", "ui.finish_round"); finding.Category != "reconciliation" {
		t.Fatalf("uncertain writes must still require reconciliation: %+v", finding)
	}
}

func TestDiagnosticsRestoreReconciledFactsForProgress(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	ref := "write-1"
	id := reactcore.DeterministicInvocationID(state.RunID, ref)
	unknown, reconciled := agentcontract.NewID(), agentcontract.NewID()
	state.Status = "completed"
	state.Facts = []agentcontract.Fact{
		{FactID: unknown, SourceCapability: "ui.command_status", Value: agentcontract.JSON{"data": agentcontract.JSON{"command_id": id, "status": "unknown"}}},
		{FactID: reconciled, SourceCapability: "ui.command_status", Quality: "verified_reconciliation", Value: agentcontract.JSON{"data": agentcontract.JSON{"command_id": id, "status": "succeeded"}}},
	}
	state.Observations = []agentcontract.Observation{
		{CallRef: "poll-" + id + "-1", Capability: "ui.command_status", Status: "succeeded", FactID: agentcontract.Strptr(unknown)},
		{CallRef: ref, Capability: "ui.update_record", Status: "succeeded", FactID: agentcontract.Strptr(reconciled)},
	}
	run, err = h.Store.Claim(run.RunID, "alice", 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.save(run, state, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	before, err := h.Get(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	report, err := h.GetDiagnostics(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if report.Progress.CompletedCount != 1 || len(report.Progress.Completed) != 1 || *report.Progress.Completed[0].FactID != reconciled {
		t.Fatalf("diagnostics did not use persisted original completion evidence: %+v", report.Progress)
	}
	after, err := h.Get(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != after.Revision {
		t.Fatal("diagnostic read changed the checkpoint")
	}
}

func TestDiagnosticsDistinguishRecoveredFailuresAndUncertainWrites(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	args := agentcontract.JSON{"id": "R-1"}
	state.Status = "completed"
	state.Observations = []agentcontract.Observation{
		{CallRef: "first", Capability: "ui.read", Arguments: args, Status: "failed", ErrorCode: agentcontract.Strptr("browser_context_required")},
		{CallRef: "retry", Capability: "ui.read", Arguments: args, Status: "succeeded"},
		{CallRef: "unrelated", Capability: "ui.read", Arguments: agentcontract.JSON{"id": "R-2"}, Status: "failed", ErrorCode: agentcontract.Strptr("upstream_response_invalid")},
		{CallRef: "final", Capability: "agent.final", Status: "rejected", ErrorCode: agentcontract.Strptr("final_result_refs_invalid")},
		{CallRef: "uncertain", Capability: "ui.write", Arguments: args, Status: "failed", ErrorCode: agentcontract.Strptr("browser_handler_outcome_unknown")},
		{CallRef: "other-write", Capability: "ui.write", Arguments: args, Status: "succeeded"},
	}
	state.Pending = []agentcontract.Invocation{{InvocationID: agentcontract.NewID(), Call: agentcontract.ToolCall{CallRef: "uncertain", Capability: "ui.write", Arguments: args}, Status: "unknown", ErrorCode: agentcontract.Strptr("browser_handler_outcome_unknown")}}
	run, err = h.Store.Claim(run.RunID, "alice", 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.save(run, state, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	report, err := h.GetDiagnostics(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	findings := map[string]agentcontract.DiagnosticFinding{}
	for _, finding := range report.Findings {
		findings[finding.Code] = finding
	}
	for _, code := range []string{"browser_context_required", "final_result_refs_invalid"} {
		if f := findings[code]; !f.Recovered || f.Actionable {
			t.Fatal("recovered evidence", f)
		}
	}
	if findings["final_result_refs_invalid"].Category != "completion" {
		t.Fatal("completion classification")
	}
	for _, code := range []string{"upstream_response_invalid", "browser_handler_outcome_unknown"} {
		if f := findings[code]; f.Recovered || !f.Actionable {
			t.Fatal("unresolved evidence", f)
		}
	}
}
