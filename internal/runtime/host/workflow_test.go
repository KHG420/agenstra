package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func workflowRef(path string) agentcontract.JSON { return agentcontract.JSON{"$workflow": path} }

func workflowFixture(t *testing.T) (*AgentHost, *hostProvider) {
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{}}
	for _, name := range []string{"records.get", "records.save"} {
		p.caps[name] = agentcontract.CapabilityDescription{Name: name, Version: "1", InputSchema: agentcontract.JSON{"type": "object"}, OutputSchema: agentcontract.JSON{"type": "object"}, Effect: "read", Replay: "safe", ReferenceScope: "durable"}
	}
	p.hook = func(_ context.Context, _ string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"value": 7, "text": "Report complete", "args": args}, ReferenceScope: "durable"}, nil
	}
	h := NewAgentHost(testStore(t), func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return p, nil }, &hostModel{}, func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{"records.get": true, "records.save": true, "workflows.list": true, "workflows.start": true, "workflows.status": true, "workflows.cancel": true}, AllowModelData: true}, nil
	})
	h.Workflows = map[string][]agentcontract.WorkflowDefinition{"records": {{Name: "report", Version: "1", Description: "Produce a report", InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []string{"id"}, "additionalProperties": false}, Steps: []agentcontract.WorkflowStep{
		{ID: "collect", Capability: "records.get", Arguments: agentcontract.JSON{"id": workflowRef("/input/id")}},
		{ID: "save", DependsOn: []string{"collect"}, Capability: "records.save", Arguments: agentcontract.JSON{"value": workflowRef("/steps/collect/data/value")}},
	}, Output: agentcontract.JSON{"value": workflowRef("/steps/save/data/value")}, Answer: workflowRef("/steps/save/data/text")}}}
	return h, p
}

func startWorkflow(t *testing.T, h *AgentHost, request string) agentcontract.StoredRun {
	t.Helper()
	run, err := h.CreateWorkflow(t.Context(), "alice", "records", "report", agentcontract.JSON{"id": "R-1"}, request)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestWorkflowCheckpointIdempotencyAndFrozenDefinition(t *testing.T) {
	h, p := workflowFixture(t)
	run := startWorkflow(t, h, "one")
	if p.calls != 0 {
		t.Fatal("creation executed business IO")
	}
	h.Workflows["records"][0].Version = "2"
	h.Workflows["records"][0].Steps[1].Capability = "removed.after.upgrade"
	duplicate := startWorkflow(t, h, "one")
	if duplicate.RunID != run.RunID {
		t.Fatal("lost stable workflow identity")
	}
	if _, err := h.CreateWorkflow(t.Context(), "alice", "records", "report", agentcontract.JSON{"id": "R-2"}, "one"); agentcontract.ErrorCode(err) != "request_id_conflict" {
		t.Fatal(err)
	}
	if _, err := h.GetWorkflow(t.Context(), run.RunID, "bob"); err == nil {
		t.Fatal("cross-owner workflow visible")
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatal(run.Status, err)
	}
	view, err := h.GetWorkflow(t.Context(), run.RunID, "alice")
	if err != nil || view.Version != "1" || view.Answer != "Report complete" || view.Output["value"] != json.Number("7") || p.calls != 2 {
		t.Fatal(view, p.calls, err)
	}
	restarted := NewAgentHost(h.Store, h.ProviderFactory, h.Model, h.PolicyResolver)
	view, err = restarted.GetWorkflow(t.Context(), run.RunID, "alice")
	if err != nil || view.Status != "completed" {
		t.Fatal(view, err)
	}
	if _, err := restarted.Drive(t.Context(), run.RunID, "alice"); err != nil || p.calls != 2 {
		t.Fatal("completed nodes replayed", p.calls, err)
	}
	state, err := h.Restore(run)
	if err != nil || len(state.ModelCalls) != 0 {
		t.Fatal("deterministic steps called a model", err)
	}
}

func TestWorkflowExactApprovalAndRevocation(t *testing.T) {
	h, p := workflowFixture(t)
	cap := p.caps["records.save"]
	cap.Effect = "write"
	cap.Replay = "idempotent"
	cap.ApprovalRequired = true
	p.caps[cap.Name] = cap
	run := startWorkflow(t, h, "approval")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_approval" || p.calls != 1 {
		t.Fatal(run.Status, p.calls, err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	item := state.Pending[0]
	if item.Call.Capability != "records.save" || item.Call.Arguments["value"] != json.Number("7") {
		t.Fatal(item)
	}
	if _, err := h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, "wrong", run.Revision, true); agentcontract.ErrorCode(err) != "approval_arguments_changed" {
		t.Fatal(err)
	}
	run, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	original := h.PolicyResolver
	h.PolicyResolver = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
		policy, err := original(ctx, owner, pack)
		policy.GrantedCapabilities["records.save"] = false
		return policy, err
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_authorization" || p.calls != 1 {
		t.Fatal(run.Status, p.calls, err)
	}
	h.PolicyResolver = original
	run, err = h.ResumeWorkflow(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "queued" || p.calls != 1 {
		t.Fatal("resume did not queue", run.Status, p.calls, err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 2 {
		t.Fatal(run.Status, p.calls, err)
	}
}

func TestWorkflowUnknownWriteReconcilesWithoutReplay(t *testing.T) {
	h, p := workflowFixture(t)
	cap := p.caps["records.save"]
	cap.Effect = "write"
	cap.Replay = "never"
	p.caps[cap.Name] = cap
	original := p.hook
	writes := 0
	p.hook = func(ctx context.Context, name string, args agentcontract.JSON, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		if name == "records.save" {
			writes++
			return agentcontract.CapabilityResult{ErrorCode: "upstream_unavailable"}, nil
		}
		return original(ctx, name, args, inv)
	}
	run := startWorkflow(t, h, "uncertain")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_reconciliation" || writes != 1 {
		t.Fatal(run.Status, writes, err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	item := state.Pending[0]
	h.Reconciler = func(_ context.Context, verification agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
		if verification.Invocation.InvocationID != item.InvocationID {
			t.Error("invocation identity changed")
		}
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"value": 7, "text": "Report complete"}}, nil
	}
	run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || writes != 1 {
		t.Fatal(run.Status, writes, err)
	}
}

func TestFrameworkWorkflowStartReconcilesOriginalChild(t *testing.T) {
	h, provider := workflowFixture(t)
	h.Model = &hostModel{decisions: []agentcontract.Decision{
		{Schema: "agenstra.decision.v1", Kind: "inspect_capability", Name: "workflows.start"},
		{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "report", Capability: "workflows.start", Arguments: agentcontract.JSON{"workflow": "report", "input": agentcontract.JSON{"id": "R-1"}}, Reason: "Generate the requested report"}}},
	}}
	run, err := h.Create(t.Context(), "alice", "records", "Generate a report", "workflow-parent")
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
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
	state.Status, state.ErrorCode = "needs_reconciliation", agentcontract.Strptr("provider_outcome_unknown")
	state.Pending[0].Status = "unknown"
	item := state.Pending[0]
	run, err = h.save(run, state, "", nil, agentcontract.JSON{"kind": "reconciliation_required"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Release(run.RunID, "alice", run.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision); agentcontract.ErrorCode(err) != "reconciliation_unavailable" || provider.calls != 0 {
		t.Fatal("unsettled child was accepted or repeated", provider.calls, err)
	}
	childID := requestRunID("alice", "workflow:"+item.InvocationID)
	child, err := h.Drive(t.Context(), childID, "alice")
	if err != nil || child.Status != "completed" || provider.calls != 2 {
		t.Fatal(child.Status, provider.calls, err)
	}
	original := h.PolicyResolver
	h.PolicyResolver = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
		policy, err := original(ctx, owner, pack)
		policy.GrantedCapabilities["workflows.start"] = false
		return policy, err
	}
	if _, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision); agentcontract.ErrorCode(err) != "capability_not_granted" {
		t.Fatal("reconciliation bypassed current authorization", err)
	}
	h.PolicyResolver = original
	run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || provider.calls != 2 {
		t.Fatal("reconciliation repeated the original child", run.Status, provider.calls, err)
	}
}

type workflowConcurrentProvider struct{ *hostProvider }

func (*workflowConcurrentProvider) ConcurrentInvocation(string) bool { return true }

func TestWorkflowIndependentNodesUseBoundedParallelExecution(t *testing.T) {
	h, p := workflowFixture(t)
	definition := h.Workflows["records"][0]
	definition.Steps = []agentcontract.WorkflowStep{{ID: "left", Capability: "records.get", Arguments: agentcontract.JSON{}}, {ID: "right", Capability: "records.get", Arguments: agentcontract.JSON{}}}
	definition.Output = agentcontract.JSON{"left": workflowRef("/steps/left/data/value"), "right": workflowRef("/steps/right/data/value")}
	definition.Answer = "Done"
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	both := make(chan struct{})
	entered := make(chan struct{}, 2)
	p.hook = func(ctx context.Context, _ string, _ agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		entered <- struct{}{}
		select {
		case <-both:
			return agentcontract.CapabilityResult{Data: agentcontract.JSON{"value": 7}}, nil
		case <-ctx.Done():
			return agentcontract.CapabilityResult{}, ctx.Err()
		}
	}
	h.ProviderFactory = func(context.Context, string, string) (agentcontract.CapabilityProvider, error) {
		return &workflowConcurrentProvider{p}, nil
	}
	run := startWorkflow(t, h, "parallel")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	type result struct {
		run agentcontract.StoredRun
		err error
	}
	finished := make(chan result, 1)
	go func() {
		run, err := h.Drive(ctx, run.RunID, "alice")
		finished <- result{run, err}
	}()
	defer func() {
		cancel()
		<-finished
	}()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			close(both)
			t.Fatal("independent nodes did not start together")
		}
	}
	close(both)
	outcome := <-finished
	finished <- outcome
	if outcome.err != nil || outcome.run.Status != "completed" || p.calls != 2 {
		t.Fatal(outcome.run.Status, p.calls, outcome.err)
	}
}

func TestWorkflowAgentChildUsesExistingRunAndSharedBudgets(t *testing.T) {
	h, p := workflowFixture(t)
	definition := h.Workflows["records"][0]
	definition.Steps = []agentcontract.WorkflowStep{{ID: "collect", Capability: "records.get", Arguments: agentcontract.JSON{}}, {ID: "analyze", DependsOn: []string{"collect"}, Agent: &agentcontract.WorkflowAgent{Instruction: "Analyze the given record", Capabilities: []string{}}, Arguments: agentcontract.JSON{"record": workflowRef("/steps/collect/data/value")}}}
	definition.Output = agentcontract.JSON{"analysis": workflowRef("/steps/analyze/answer_markdown")}
	definition.Answer = workflowRef("/steps/analyze/answer_markdown")
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	run := startWorkflow(t, h, "agent")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" {
		t.Fatal(run.Status, err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	childID := state.Workflow.Steps[1].RunID
	child, err := h.Get(t.Context(), childID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := h.ProjectPolicy(t.Context(), child)
	if err != nil || len(policy.GrantedCapabilities) != 0 {
		t.Fatal("agent escaped its declared tool scope", policy, err)
	}
	child, err = h.Drive(t.Context(), childID, "alice")
	if err != nil || child.Status != "completed" {
		t.Fatal(child.Status, err)
	}
	wake := run.NextWakeAt
	h.Clock = func() float64 { return *wake + 1 }
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 1 {
		t.Fatal(run.Status, p.calls, err)
	}
	state, err = h.Restore(run)
	if err != nil || state.AnswerMarkdown != "Done" || state.RoundsUsed != 1 || len(state.ModelCalls) != 0 {
		t.Fatal(state, err)
	}
	telemetry, err := h.Telemetry(run)
	if err != nil || telemetry.Budget.Tokens.ChargedTokens != state.ModelUsage.BudgetTokens || telemetry.Budget.Tokens.ChargedTokens == 0 {
		t.Fatal("workflow telemetry lost child usage", telemetry.Budget.Tokens, err)
	}
}

func TestWorkflowCancelStopsQueuedAgentChild(t *testing.T) {
	h, _ := workflowFixture(t)
	definition := h.Workflows["records"][0]
	definition.Steps = []agentcontract.WorkflowStep{{ID: "agent", Agent: &agentcontract.WorkflowAgent{Instruction: "Analyze", Capabilities: []string{}}}}
	definition.Output = nil
	definition.Answer = "Done"
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	run := startWorkflow(t, h, "cancel")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	childID := state.Workflow.Steps[0].RunID
	if _, err := h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	child, err := h.Store.GetRun(childID, "alice")
	if err != nil || !child.CancelRequested {
		t.Fatal("cancel left the child runnable", child.Status, err)
	}
}

func TestWorkflowConditionalIndependentNodePreservesPendingWork(t *testing.T) {
	h, p := workflowFixture(t)
	definition := h.Workflows["records"][0]
	definition.Steps = []agentcontract.WorkflowStep{{ID: "first", Capability: "records.get", Arguments: agentcontract.JSON{}}, {ID: "skipped", Capability: "records.get", Arguments: agentcontract.JSON{}, When: &agentcontract.WorkflowCondition{Value: false, Equals: true}}}
	definition.Output = agentcontract.JSON{"value": workflowRef("/steps/first/data/value"), "skipped": workflowRef("/steps/skipped/data/value")}
	definition.Answer = "Done"
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	run := startWorkflow(t, h, "conditional")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 1 {
		t.Fatal(run.Status, p.calls, err)
	}
	view, err := h.GetWorkflow(t.Context(), run.RunID, "alice")
	if err != nil || view.Steps[1].Status != "skipped" || view.Output["skipped"] != nil {
		t.Fatal(view, err)
	}
}

func TestWorkflowAsynchronousNodeWaitsWithoutResubmission(t *testing.T) {
	h, p := workflowFixture(t)
	cap := p.caps["records.get"]
	cap.Operation = &agentcontract.OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "records.save", PollArgument: []string{"id"}, PendingStates: []string{"running"}, SuccessStates: []string{"completed"}, FailureStates: []string{"failed"}, IntervalSeconds: 1, TimeoutSeconds: 60}
	p.caps[cap.Name] = cap
	definition := h.Workflows["records"][0]
	definition.Steps = definition.Steps[:1]
	definition.Output = agentcontract.JSON{"value": workflowRef("/steps/collect/data/value")}
	definition.Answer = "Done"
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	starts, polls := 0, 0
	p.hook = func(_ context.Context, name string, _ agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		status := "running"
		if name == "records.get" {
			starts++
		} else {
			polls++
			status = "completed"
		}
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "job-one", "status": status, "value": 7}, ReferenceScope: "durable"}, nil
	}
	run := startWorkflow(t, h, "async")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" || starts != 1 || polls != 0 {
		t.Fatal(run.Status, starts, polls, err)
	}
	restarted := NewAgentHost(h.Store, h.ProviderFactory, h.Model, h.PolicyResolver)
	wake := *run.NextWakeAt
	restarted.Clock = func() float64 { return wake + 1 }
	run, err = restarted.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || starts != 1 || polls != 1 {
		t.Fatal(run.Status, starts, polls, err)
	}
}

func TestWorkflowToolReferenceRevalidatedAfterApproval(t *testing.T) {
	h, p := workflowFixture(t)
	read := p.caps["records.get"]
	read.ReferenceScope = "connection"
	p.caps[read.Name] = read
	write := p.caps["records.save"]
	write.Effect, write.Replay, write.ApprovalRequired = "write", "never", true
	p.caps[write.Name] = write
	original := p.hook
	p.hook = func(ctx context.Context, name string, args agentcontract.JSON, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		result, err := original(ctx, name, args, inv)
		if name == "records.get" {
			result.ReferenceScope = "connection"
		}
		return result, err
	}
	run := startWorkflow(t, h, "expired-reference")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_approval" {
		t.Fatal(run.Status, err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	item := state.Pending[0]
	if _, ok := item.OriginalArguments["value"].(map[string]any)["$fact_value"]; !ok {
		t.Fatal("tool evidence lost its reference")
	}
	run, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "failed" || p.calls != 1 {
		t.Fatal(run.Status, p.calls, err)
	}
	state, err = h.Restore(run)
	if err != nil || state.ErrorCode == nil || *state.ErrorCode != "fact_reference_expired" {
		t.Fatal(state.ErrorCode, err)
	}
}

func TestWorkflowCannotReadFieldsWithheldByModelOutput(t *testing.T) {
	h, p := workflowFixture(t)
	read := p.caps["records.get"]
	read.ModelOutput = &agentcontract.ModelOutput{Paths: [][]string{{"text"}}}
	p.caps[read.Name] = read
	run := startWorkflow(t, h, "withheld")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "failed" || p.calls != 1 {
		t.Fatal(run.Status, p.calls, err)
	}
}

func TestWorkflowCancelInterruptsRunningChildModel(t *testing.T) {
	h, _ := workflowFixture(t)
	definition := h.Workflows["records"][0]
	definition.Steps = []agentcontract.WorkflowStep{{ID: "agent", Agent: &agentcontract.WorkflowAgent{Instruction: "Analyze", Capabilities: []string{}}}}
	definition.Output, definition.Answer = nil, "Done"
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	entered := make(chan struct{})
	h.Model = decisionModelFunc(func(ctx context.Context, _ agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		close(entered)
		<-ctx.Done()
		return agentcontract.Decision{}, ctx.Err()
	})
	run := startWorkflow(t, h, "cancel-running")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	childID := state.Workflow.Steps[0].RunID
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	finished := make(chan error, 1)
	go func() {
		child, err := h.Drive(ctx, childID, "alice")
		if err == nil && child.Status != "cancelled" {
			err = agentcontract.NewHostError("child_still_running")
		}
		finished <- err
	}()
	defer func() { cancel(); <-finished }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("child model did not start")
	}
	// Exercise the race-safe heartbeat even if cancellation reaches the parent
	// before its new child's checkpoint was published.
	if _, err := h.Store.RequestCancel(run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	err = <-finished
	finished <- err
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowAgentInputUsesExistingResumeFlow(t *testing.T) {
	h, _ := workflowFixture(t)
	definition := h.Workflows["records"][0]
	definition.Steps = []agentcontract.WorkflowStep{{ID: "agent", Agent: &agentcontract.WorkflowAgent{Instruction: "Ask for the required period", Capabilities: []string{}}}}
	definition.Output, definition.Answer = nil, workflowRef("/steps/agent/answer_markdown")
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	h.Model = &hostModel{decisions: []agentcontract.Decision{{Schema: "agenstra.decision.v1", Kind: "request_input", Field: "period", Prompt: "Which period?"}}}
	run := startWorkflow(t, h, "input")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	childID := state.Workflow.Steps[0].RunID
	child, err := h.Drive(t.Context(), childID, "alice")
	if err != nil || child.Status != "needs_input" {
		t.Fatal(child.Status, err)
	}
	view, err := h.GetWorkflow(t.Context(), run.RunID, "alice")
	if err != nil || view.BlockedRunID != childID || view.BlockedState != "needs_input" {
		t.Fatal(view, err)
	}
	if _, err := h.SupplyInput(t.Context(), childID, "alice", "period", "two days", child.Revision); err != nil {
		t.Fatal(err)
	}
	child, err = h.Drive(t.Context(), childID, "alice")
	if err != nil || child.Status != "completed" {
		t.Fatal(child.Status, err)
	}
	wake := *run.NextWakeAt
	h.Clock = func() float64 { return wake + 1 }
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatal(run.Status, err)
	}
}

func TestWorkflowRejectsNonObjectOutput(t *testing.T) {
	h, _ := workflowFixture(t)
	definition := h.Workflows["records"][0]
	definition.Steps = definition.Steps[:1]
	definition.Output = workflowRef("/steps/collect/data/value")
	definition.Answer = "Done"
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	run := startWorkflow(t, h, "invalid-output")
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "failed" {
		t.Fatal(run.Status, err)
	}
	state, err := h.Restore(run)
	if err != nil || state.ErrorCode == nil || *state.ErrorCode != "workflow_output_invalid" {
		t.Fatal(state.ErrorCode, err)
	}
}
