package host

import (
	"context"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type specialistTestProvider struct{ *hostProvider }

func (p *specialistTestProvider) SystemPrompt() string { return "PARENT_ONLY_BUSINESS_GUIDANCE" }
func (p *specialistTestProvider) Skills() map[string]agentcontract.Skill {
	return map[string]agentcontract.Skill{
		"domain": {Description: agentcontract.SkillDescription{Name: "domain"}, Content: "Specialist evidence guidance"},
		"other":  {Description: agentcontract.SkillDescription{Name: "other"}, Content: "Unrelated guidance"},
	}
}

type specialistTestModel struct{ t *testing.T }

func (m *specialistTestModel) Decide(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
	if packet.CompletionReview != nil {
		return agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "approve_completion"}, nil
	}
	if packet.Instruction == "Collect the record." {
		if strings.Contains(prompt, "PARENT_ONLY_BUSINESS_GUIDANCE") || !strings.Contains(prompt, "SPECIALIST_ONLY_GUIDANCE") || len(packet.LoadedSkills) != 1 || packet.LoadedSkills["domain"] == "" {
			m.t.Fatal("specialist instructions or skills were not isolated")
		}
		for _, capability := range packet.Capabilities {
			if capability["name"] != "records.get" {
				m.t.Fatal("specialist received another capability", capability["name"])
			}
		}
		if len(packet.Facts) == 0 {
			return callDecision("records.get"), nil
		}
	} else if len(packet.Facts) == 0 {
		for _, skill := range packet.Skills {
			if skill["name"] == "domain" {
				m.t.Fatal("coordinator still exposes the specialist skill")
			}
		}
		for _, capability := range packet.Capabilities {
			if capability["name"] == "records.get" {
				m.t.Fatal("coordinator still exposes the specialist capability")
			}
		}
		return agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "specialist-1", Capability: "workflows.start", Arguments: agentcontract.JSON{"workflow": "specialist", "input": agentcontract.JSON{"task": "Collect the record."}}, Reason: "Delegate the focused task"}}}, nil
	}
	ids := []string{}
	for _, fact := range packet.Facts {
		ids = append(ids, fact.FactID)
	}
	return agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Record value is 7.", FactIDs: ids}, nil
}

func queuedSpecialist(t *testing.T) (*AgentHost, *hostProvider, agentcontract.StoredRun, agentcontract.StoredRun, agentcontract.StoredRun) {
	t.Helper()
	h, base := workflowFixture(t)
	h.ProviderFactory = func(context.Context, string, string) (agentcontract.CapabilityProvider, error) {
		return &specialistTestProvider{base}, nil
	}
	h.Model = &specialistTestModel{t: t}
	definition, err := (agentcontract.SubagentDefinition{Name: "specialist", Version: "1", Description: "A focused record specialist", SystemPrompt: "SPECIALIST_ONLY_GUIDANCE", Capabilities: []string{"records.get"}, Skills: []string{"domain"}}).Workflow()
	if err != nil {
		t.Fatal(err)
	}
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	h.CompletionPreflight = func(_ context.Context, completion agentcontract.CompletionContext) error {
		if completion.Instruction == "Collect the record." && (len(completion.LoadedSkills) != 1 || completion.LoadedSkills[0] != "domain") {
			t.Fatal("completion lost its selected specialist skill")
		}
		return nil
	}
	parent, err := h.Create(t.Context(), "alice", "records", "Delegate the record task.", "parent")
	if err != nil {
		t.Fatal(err)
	}
	parent, err = h.Drive(t.Context(), parent.RunID, "alice")
	if err != nil || parent.Status != "waiting" {
		t.Fatal(parent.Status, err)
	}
	state, err := h.Restore(parent)
	if err != nil {
		t.Fatal(err)
	}
	workflowID := requestRunID("alice", "workflow:"+state.Pending[0].InvocationID)
	workflow, err := h.Drive(t.Context(), workflowID, "alice")
	if err != nil || workflow.Status != "waiting" {
		t.Fatal(workflow.Status, err)
	}
	progress, err := h.Restore(workflow)
	if err != nil {
		t.Fatal(err)
	}
	child, err := h.Get(t.Context(), progress.Workflow.Steps[0].RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	return h, base, parent, workflow, child
}

func TestSubagentIsolatesContextAndReturnsEvidenceWithinParentBudgets(t *testing.T) {
	h, provider, parent, workflow, child := queuedSpecialist(t)
	config, err := h.effectiveRunConfig(child)
	if err != nil || config.Settings.MaxModelRounds >= h.runSettings(parent).MaxModelRounds {
		t.Fatal("child obtained a fresh model budget", config, err)
	}
	child, err = h.Drive(t.Context(), child.RunID, "alice")
	if err != nil || child.Status != "completed" || provider.calls != 1 {
		t.Fatal(child.Status, provider.calls, err)
	}
	wake := max(*parent.NextWakeAt, *workflow.NextWakeAt) + 1
	h.Clock = func() float64 { return wake }
	workflow, err = h.Drive(t.Context(), workflow.RunID, "alice")
	if err != nil || workflow.Status != "completed" {
		t.Fatal(workflow.Status, err)
	}
	view, err := h.GetWorkflow(t.Context(), workflow.RunID, "alice")
	if err != nil || view.Output["answer_markdown"] != "Record value is 7." {
		t.Fatal(view, err)
	}
	evidence, ok := view.Output["evidence"].(map[string]any)
	if !ok || evidence["source_run_id"] != child.RunID {
		t.Fatal("delegated evidence lost its source run", evidence)
	}
	facts, ok := evidence["facts"].([]any)
	if !ok || len(facts) != 1 {
		t.Fatal("cited evidence was omitted", evidence)
	}
	parent, err = h.Drive(t.Context(), parent.RunID, "alice")
	if err != nil || parent.Status != "completed" {
		t.Fatal(parent.Status, err)
	}
	state, err := h.Restore(parent)
	if err != nil || len(state.LoadedSkills) != 0 || len(state.DelegatedModelCalls) == 0 || state.RoundsUsed < 4 {
		t.Fatal("parent lost isolation or delegated usage", state, err)
	}
	telemetry, err := h.Telemetry(parent)
	if err != nil || telemetry.Budget.Tokens.ChargedTokens != state.ModelUsage.BudgetTokens {
		t.Fatal("delegated usage differs from charged usage", telemetry, err)
	}
	before := state.ModelUsage.BudgetTokens
	parent, err = h.Drive(t.Context(), parent.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, err = h.Restore(parent)
	if err != nil || state.ModelUsage.BudgetTokens != before || provider.calls != 1 {
		t.Fatal("settled delegation was replayed or charged twice", err)
	}
}

func TestSubagentCancellationAndRevokedGrantUseExistingChildLifecycle(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		h, provider, parent, workflow, child := queuedSpecialist(t)
		if _, err := h.Cancel(t.Context(), parent.RunID, "alice"); err != nil {
			t.Fatal(err)
		}
		for _, run := range []agentcontract.StoredRun{workflow, child} {
			current, err := h.Get(t.Context(), run.RunID, "alice")
			if err != nil || !current.CancelRequested {
				t.Fatal("descendant did not receive cancellation", current, err)
			}
		}
		child, err := h.Drive(t.Context(), child.RunID, "alice")
		if err != nil || child.Status != "cancelled" || provider.calls != 0 {
			t.Fatal(child.Status, provider.calls, err)
		}
	})
	t.Run("revoke", func(t *testing.T) {
		h, provider, _, _, child := queuedSpecialist(t)
		original := h.PolicyResolver
		h.PolicyResolver = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
			policy, err := original(ctx, owner, pack)
			policy.GrantedCapabilities["records.get"] = false
			return policy, err
		}
		child, err := h.Drive(t.Context(), child.RunID, "alice")
		if err != nil || child.Status != "needs_authorization" || provider.calls != 0 {
			t.Fatal(child.Status, provider.calls, err)
		}
	})
}

func TestSubagentRejectsAnotherActiveDelegationForTheSameParent(t *testing.T) {
	h, _, parent, _, _ := queuedSpecialist(t)
	provider, err := h.OpenRunProvider(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	})
	result, err := provider.Invoke(t.Context(), "workflows.start", agentcontract.JSON{"workflow": "specialist", "input": agentcontract.JSON{"task": "Another task"}}, &agentcontract.InvocationContext{RunID: parent.RunID, OwnerID: parent.OwnerID, InvocationID: agentcontract.NewID()})
	if err != nil || result.ErrorCode != "subagent_busy" {
		t.Fatal("concurrent delegation obtained a fresh budget", result, err)
	}
}
