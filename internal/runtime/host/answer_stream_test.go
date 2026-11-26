package host

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestAnswerSnapshotsAreOwnedFencedAndDurable(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	run, err := h.Store.Claim(run.RunID, "alice", 30)
	if err != nil {
		t.Fatal(err)
	}
	old := h.answers.begin(t.Context(), run)
	agentcontract.ObserveAnswer(old, agentcontract.AnswerUpdate{Text: "old", Phase: "generating"})
	current := h.answers.begin(t.Context(), run)
	agentcontract.ObserveAnswer(current, agentcontract.AnswerUpdate{Phase: "waiting", ReasoningPreview: "核对条件"})
	if got, err := h.AnswerSnapshotWithReasoningPreview(t.Context(), run.RunID, "alice"); err != nil || got.ReasoningPreview != "核对条件" || got.Text != "" {
		t.Fatal("authorized live preview missing", got, err)
	}
	if got, err := h.AnswerSnapshot(t.Context(), run.RunID, "alice"); err != nil || got.ReasoningPreview != "" {
		t.Fatal("ordinary subscriber received reasoning", got, err)
	}
	if got, err := h.AnswerSnapshotWithReasoningPreview(t.Context(), run.RunID, "bob"); err == nil || got.ReasoningPreview != "" {
		t.Fatal("cross-owner preview", got, err)
	}
	agentcontract.ObserveAnswer(old, agentcontract.AnswerUpdate{Text: "stale", Phase: "generating"})
	if got, err := h.AnswerSnapshot(t.Context(), run.RunID, "alice"); err != nil || got.Text != "" {
		t.Fatal("superseded candidate survived", got, err)
	}
	agentcontract.ObserveAnswer(current, agentcontract.AnswerUpdate{Text: "draft", Phase: "checking"})
	if _, err := h.AnswerSnapshot(t.Context(), run.RunID, "bob"); err == nil {
		t.Fatal("cross-owner disclosure")
	}
	if got, err := h.AnswerSnapshot(t.Context(), run.RunID, "alice"); err != nil || got.Phase != "checking" || got.Text != "draft" {
		t.Fatal(got, err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	state.Status = "completed"
	state.AnswerMarkdown = "validated answer"
	if _, err = h.save(run, state, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Reconnect to a new host instance with no transient cache.
	restarted := testHost(t, h.Store, &hostProvider{}, &hostModel{})
	if got, err := restarted.AnswerSnapshot(t.Context(), run.RunID, "alice"); err != nil || got.Text != "validated answer" || got.Phase != "completed" {
		t.Fatal("completion not durable", got, err)
	}
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("access_denied")
	}
	if got, err := h.AnswerSnapshot(t.Context(), run.RunID, "alice"); err == nil || got.Text != "" {
		t.Fatal("revoked access retained answer", got, err)
	}
}

func TestAnswerCancellationEvictionAndBounds(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	run, err := h.Store.Claim(run.RunID, "alice", 30)
	if err != nil {
		t.Fatal(err)
	}
	ctx := h.answers.begin(t.Context(), run)
	agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: "draft", Phase: "generating"})
	if _, err = h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: "late", Phase: "generating"})
	if got, err := h.AnswerSnapshot(t.Context(), run.RunID, "alice"); err != nil || got.Text != "" || got.Phase != "cancelled" {
		t.Fatal(got, err)
	}
	if len(h.answers.entries) != 0 {
		t.Fatal("cancelled candidate retained")
	}
	for i := 0; i < 140; i++ {
		h.answers.begin(t.Context(), agentcontract.StoredRun{RunID: fmt.Sprint(i), OwnerID: "alice", LeaseToken: "lease"})
	}
	if len(h.answers.entries) != 128 {
		t.Fatal("unbounded candidates", len(h.answers.entries))
	}
	run.RunID = "bounded"
	ctx = h.answers.begin(t.Context(), run)
	agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: strings.Repeat("x", 120001), Phase: "generating"})
	entry := h.answers.entries[answerKey{"alice", run.RunID}]
	if entry.update.Text != "" {
		t.Fatal("unbounded answer")
	}
	entry.updated = time.Now().Add(-11 * time.Minute)
	h.answers.begin(t.Context(), agentcontract.StoredRun{RunID: "new", OwnerID: "alice"})
	agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: "late expired text", Phase: "generating"})
	if h.answers.entries[answerKey{"alice", run.RunID}] != nil {
		t.Fatal("expired generation revived")
	}
}

func TestAnswerPreviewFollowsActiveDelegatedWorkflow(t *testing.T) {
	h, _ := workflowFixture(t)
	definition := h.Workflows["records"][0]
	definition.Steps = []agentcontract.WorkflowStep{{ID: "agent", Agent: &agentcontract.WorkflowAgent{
		Instruction: "Analyze the report", Capabilities: []string{"records.get"},
	}}}
	definition.Output, definition.Answer = nil, workflowRef("/steps/agent/answer_markdown")
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	h.Model = &hostModel{decisions: []agentcontract.Decision{
		{Schema: "agenstra.decision.v1", Kind: "inspect_capability", Name: "workflows.start"},
		{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{
			CallRef: "report", Capability: "workflows.start", Reason: "Analyze the report",
			Arguments: agentcontract.JSON{"workflow": "report", "input": agentcontract.JSON{"id": "R-1"}},
		}}},
	}}
	root, err := h.Create(t.Context(), "alice", "records", "Analyze the report", "preview-root")
	if err != nil {
		t.Fatal(err)
	}
	root, err = h.Drive(t.Context(), root.RunID, "alice")
	if err != nil || root.Status != "waiting" {
		t.Fatal(root.Status, err)
	}
	state, err := h.Restore(root)
	if err != nil {
		t.Fatal(err)
	}
	workflowID := requestRunID(root.OwnerID, "workflow:"+state.Pending[0].InvocationID)
	workflow, err := h.Drive(t.Context(), workflowID, root.OwnerID)
	if err != nil || workflow.Status != "waiting" {
		t.Fatal(workflow.Status, err)
	}
	progress, err := h.Restore(workflow)
	if err != nil {
		t.Fatal(err)
	}
	child, err := h.Store.Claim(progress.Workflow.Steps[0].RunID, root.OwnerID, h.Settings.LeaseSeconds)
	if err != nil {
		t.Fatal(err)
	}
	stepCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stepCtx = h.answers.begin(stepCtx, child)
	agentcontract.ObserveAnswer(stepCtx, agentcontract.AnswerUpdate{Phase: "waiting", ReasoningPreview: "逐项核对日报"})
	got, err := h.AnswerSnapshotWithReasoningPreview(t.Context(), root.RunID, root.OwnerID)
	if err != nil || got.RunID != root.RunID || got.Phase != "waiting" || got.Text != "" || got.ReasoningPreview != "逐项核对日报" {
		t.Fatal("root preview missing", got, err)
	}
	if got, err = h.AnswerSnapshot(t.Context(), root.RunID, root.OwnerID); err != nil || got.ReasoningPreview != "" {
		t.Fatal("ordinary subscriber received delegated reasoning", got, err)
	}
	if _, err = h.AnswerSnapshotWithReasoningPreview(t.Context(), root.RunID, "bob"); err == nil {
		t.Fatal("delegated reasoning crossed owners")
	}
	originalPolicy := h.PolicyResolver
	h.PolicyResolver = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
		policy, err := originalPolicy(ctx, owner, pack)
		policy.GrantedCapabilities["records.get"] = false
		return policy, err
	}
	if got, err = h.AnswerSnapshotWithReasoningPreview(t.Context(), root.RunID, root.OwnerID); err != nil || got.ReasoningPreview != "" {
		t.Fatal("revoked delegate remained visible", got, err)
	}
	h.PolicyResolver = originalPolicy
	agentcontract.ObserveAnswer(stepCtx, agentcontract.AnswerUpdate{Phase: "generating", Text: "specialist draft"})
	if got, err = h.AnswerSnapshotWithReasoningPreview(t.Context(), root.RunID, root.OwnerID); err != nil || got.Text != "" || got.ReasoningPreview != "" {
		t.Fatal("specialist draft or stale preview reached root", got, err)
	}
	agentcontract.ObserveAnswer(stepCtx, agentcontract.AnswerUpdate{Phase: "waiting", ReasoningPreview: "继续核对"})
	cancel()
	if got, err = h.AnswerSnapshotWithReasoningPreview(t.Context(), root.RunID, root.OwnerID); err != nil || got.ReasoningPreview != "" {
		t.Fatal("stopped delegate remained visible", got, err)
	}
}
