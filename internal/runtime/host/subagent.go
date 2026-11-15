package host

import (
	"context"
	"errors"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

type workflowAgentProvider struct {
	agentcontract.CapabilityProvider
	caps   map[string]agentcontract.CapabilityDescription
	skills map[string]agentcontract.Skill
	prompt string
}

func (p *workflowAgentProvider) Capabilities() map[string]agentcontract.CapabilityDescription {
	return p.caps
}
func (p *workflowAgentProvider) Skills() map[string]agentcontract.Skill { return p.skills }
func (p *workflowAgentProvider) SystemPrompt() string                   { return p.prompt }
func (p *workflowAgentProvider) ConcurrentInvocation(name string) bool {
	provider, ok := p.CapabilityProvider.(agentcontract.ConcurrentCapabilityProvider)
	return ok && provider.ConcurrentInvocation(name)
}
func (p *workflowAgentProvider) ValidateInvocation(ctx context.Context, name string, inv agentcontract.InvocationContext) error {
	if validator, ok := p.CapabilityProvider.(agentcontract.InvocationValidator); ok {
		return validator.ValidateInvocation(ctx, name, inv)
	}
	return nil
}

func workflowAgentScope(run agentcontract.StoredRun) (agentcontract.WorkflowParent, error) {
	var parent agentcontract.WorkflowParent
	runtime, _ := run.State["runtime"].(map[string]any)
	if runtime["workflow_parent"] == nil {
		return parent, nil
	}
	raw, err := agentcontract.CanonicalJSON(runtime["workflow_parent"])
	if err != nil || jsonvalue.DecodeStrict(raw, &parent) != nil {
		return parent, agentcontract.NewHostError("workflow_checkpoint_invalid")
	}
	return parent, nil
}

func scopedWorkflowAgent(base agentcontract.CapabilityProvider, parent agentcontract.WorkflowParent) (agentcontract.CapabilityProvider, error) {
	p := &workflowAgentProvider{CapabilityProvider: base, caps: map[string]agentcontract.CapabilityDescription{}, skills: map[string]agentcontract.Skill{}, prompt: parent.SystemPrompt}
	for _, name := range parent.Capabilities {
		capability, exists := base.Capabilities()[name]
		if !exists {
			return nil, agentcontract.NewHostError("capability_unknown")
		}
		p.caps[name] = capability
	}
	for _, name := range parent.Skills {
		skill, exists := base.Skills()[name]
		if !exists {
			return nil, agentcontract.NewHostError("skill_unknown")
		}
		p.skills[name] = skill
	}
	return p, nil
}

func subagentEvidence(child agentcontract.StoredRun, state *agentcontract.RuntimeState) (agentcontract.JSON, error) {
	cited := []string{}
	for i := len(state.Decisions) - 1; i >= 0; i-- {
		if state.Decisions[i]["kind"] != "final" {
			continue
		}
		raw, err := agentcontract.CanonicalJSON(state.Decisions[i]["fact_ids"])
		if err != nil || jsonvalue.DecodeStrict(raw, &cited) != nil {
			return nil, agentcontract.NewHostError("workflow_checkpoint_invalid")
		}
		break
	}
	wanted := map[string]bool{}
	for _, id := range cited {
		wanted[id] = true
	}
	facts := []agentcontract.JSON{}
	for _, fact := range state.Facts {
		if wanted[fact.FactID] {
			facts = append(facts, agentcontract.JSON{"source_fact_id": fact.FactID, "capability": fact.SourceCapability, "observed_at": fact.ObservedAt, "value": agentcontract.ModelFactValue(fact)})
		}
	}
	return agentcontract.JSON{"source_run_id": child.RunID, "facts": facts}, nil
}

// A delegated specialist occupies one child slot per parent. Stable invocation
// IDs preserve retries; other workflow definitions keep their existing behavior.
func (h *AgentHost) subagentSlot(parent agentcontract.StoredRun, candidateID string) error {
	state, err := h.Restore(parent)
	if err != nil {
		return err
	}
	for _, item := range state.Pending {
		if item.Call.Capability != "workflows.start" {
			continue
		}
		id := requestRunID(parent.OwnerID, "workflow:"+item.InvocationID)
		if id == candidateID {
			continue
		}
		child, err := h.Store.GetRun(id, parent.OwnerID)
		if errors.Is(err, runstore.ErrRunNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		progress, err := h.Restore(child)
		if err != nil {
			return err
		}
		if progress.Workflow != nil && progress.Workflow.Definition.Subagent && !Terminal(child.Status) {
			return agentcontract.NewHostError("subagent_busy")
		}
	}
	return nil
}

// Called immediately before settled invocations are cleared and checkpointed.
// The charge and cleared batch are saved atomically, so a resumed batch cannot
// charge a completed child twice or obtain a fresh parent budget.
func (h *AgentHost) accountSubagentUsage(run agentcontract.StoredRun, state *agentcontract.RuntimeState) error {
	for _, item := range state.Pending {
		if item.Call.Capability != "workflows.start" {
			continue
		}
		id := requestRunID(run.OwnerID, "workflow:"+item.InvocationID)
		child, err := h.Store.GetRun(id, run.OwnerID)
		if errors.Is(err, runstore.ErrRunNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		progress, err := h.Restore(child)
		if err != nil {
			return err
		}
		if progress.Workflow == nil || !progress.Workflow.Definition.Subagent {
			continue
		}
		if progress.WorkflowParent == nil || progress.WorkflowParent.RunID != run.RunID || !Terminal(child.Status) {
			return agentcontract.NewHostError("workflow_checkpoint_invalid")
		}
		state.RoundsUsed += progress.RoundsUsed
		state.ToolCallsUsed += progress.ToolCallsUsed
		state.PollCallsUsed += progress.PollCallsUsed
		for _, call := range progress.Workflow.AgentModelCalls {
			reactcore.AddModelUsage(&state.ModelUsage, call)
			state.DelegatedModelCalls = append(state.DelegatedModelCalls, call)
		}
	}
	return nil
}
