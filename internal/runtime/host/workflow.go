package host

import (
	"context"
	"errors"
	"strings"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

// ListWorkflows returns the configured catalog after checking current owner access.
func (h *AgentHost) ListWorkflows(ctx context.Context, owner, pack string) ([]agentcontract.WorkflowInfo, error) {
	policy, err := h.Policy(ctx, owner, pack, false)
	if err != nil {
		return nil, err
	}
	if !policy.GrantedCapabilities["workflows.list"] {
		return nil, agentcontract.NewHostError("capability_not_granted")
	}
	return authorizedWorkflowInfos(h.Workflows[pack], policy)
}

func authorizedWorkflowInfos(definitions []agentcontract.WorkflowDefinition, policy agentcontract.ExecutionPolicy) ([]agentcontract.WorkflowInfo, error) {
	infos, err := workflowInfos(definitions)
	if err != nil {
		return nil, err
	}
	visible := []agentcontract.WorkflowInfo{}
	for i, definition := range definitions {
		if workflowAccess(definition, policy) == nil {
			visible = append(visible, infos[i])
		}
	}
	return visible, nil
}

func workflowInfos(definitions []agentcontract.WorkflowDefinition) ([]agentcontract.WorkflowInfo, error) {
	if len(definitions) > 32 {
		return nil, agentcontract.NewHostError("workflow_definition_invalid")
	}
	result := []agentcontract.WorkflowInfo{}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if definition.Validate() != nil || seen[definition.Name] {
			return nil, agentcontract.NewHostError("workflow_definition_invalid")
		}
		seen[definition.Name] = true
		result = append(result, agentcontract.WorkflowInfo{Name: definition.Name, Version: definition.Version, Description: definition.Description, InputSchema: definition.InputSchema})
	}
	return jsonvalue.Clone(result)
}

// CreateWorkflow validates and queues a trusted workflow without running business IO.
// A request ID is required and remains stable across retries and definition updates.
func (h *AgentHost) CreateWorkflow(ctx context.Context, owner, pack, name string, input agentcontract.JSON, requestID string) (agentcontract.StoredRun, error) {
	return h.createWorkflow(ctx, owner, pack, name, input, requestID, h.Workflows[pack], nil)
}

func (h *AgentHost) createWorkflow(ctx context.Context, owner, pack, name string, input agentcontract.JSON, requestID string, definitions []agentcontract.WorkflowDefinition, parent *agentcontract.StoredRun) (agentcontract.StoredRun, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 128 || input == nil {
		return agentcontract.StoredRun{}, agentcontract.NewHostError("workflow_request_invalid")
	}
	policy, err := h.Policy(ctx, owner, pack, true)
	if err == nil && parent != nil {
		policy, err = h.ProjectPolicy(ctx, *parent)
	}
	if err != nil {
		return agentcontract.StoredRun{}, err
	}
	if !policy.GrantedCapabilities["workflows.start"] {
		return agentcontract.StoredRun{}, agentcontract.NewHostError("capability_not_granted")
	}
	id := requestRunID(owner, requestID)
	if existing, err := h.Store.GetRun(id, owner); err == nil {
		return h.matchWorkflow(existing, pack, name, input)
	} else if !errors.Is(err, runstore.ErrRunNotFound) {
		return existing, err
	}
	if _, err := workflowInfos(definitions); err != nil {
		return agentcontract.StoredRun{}, err
	}
	var definition *agentcontract.WorkflowDefinition
	for _, candidate := range definitions {
		if candidate.Name == name {
			copy := candidate
			definition = &copy
			break
		}
	}
	if definition == nil {
		return agentcontract.StoredRun{}, agentcontract.NewHostError("workflow_unknown")
	}
	inputBytes, err := agentcontract.CanonicalJSON(input)
	if err != nil || len(inputBytes) > 256<<10 {
		return agentcontract.StoredRun{}, agentcontract.NewHostError("workflow_input_invalid")
	}
	validator, err := agentcontract.ValidateLocalSchema(definition.InputSchema, false)
	if err != nil || agentcontract.ValidateSchema(validator, input) != nil {
		return agentcontract.StoredRun{}, agentcontract.NewHostError("workflow_input_invalid")
	}
	if err := workflowAccess(*definition, policy); err != nil {
		return agentcontract.StoredRun{}, err
	}
	instruction := definition.Description + "\nWorkflow: " + name
	var envelope map[string]any
	if parent == nil {
		envelope, err = h.prepareRun(ctx, owner, pack, instruction, id)
	} else {
		envelope, err = h.prepareWorkflowChild(ctx, *parent, instruction, id, nil)
	}
	if err != nil {
		return agentcontract.StoredRun{}, err
	}
	delete(envelope, "memory_inputs")
	state := &agentcontract.WorkflowExecution{Definition: *definition, Input: input, Steps: []agentcontract.WorkflowStepState{}}
	for _, step := range definition.Steps {
		state.Steps = append(state.Steps, agentcontract.WorkflowStepState{ID: step.ID, Status: "pending"})
	}
	runtime := envelope["runtime"].(map[string]any)
	runtime["workflow"] = state
	if parent != nil {
		runtime["workflow_parent"] = agentcontract.WorkflowParent{RunID: parent.RunID}
	}
	raw, err := agentcontract.CanonicalJSON(envelope)
	limit := NormalizedRunSettings(h.Settings).MaxStateBytes
	if parent != nil {
		limit = h.runSettings(*parent).MaxStateBytes
	}
	if err != nil || len(raw) > limit {
		return agentcontract.StoredRun{}, agentcontract.NewHostError("run_state_too_large")
	}
	created, err := h.Store.CreateRun(owner, pack, envelope, id)
	if errors.Is(err, runstore.ErrStoreConflict) {
		created, err = h.Store.GetRun(id, owner)
		if err == nil {
			return h.matchWorkflow(created, pack, name, input)
		}
	}
	return created, err
}

func workflowAccess(definition agentcontract.WorkflowDefinition, policy agentcontract.ExecutionPolicy) error {
	for _, step := range definition.Steps {
		names := []string{step.Capability}
		if step.Agent != nil {
			names = step.Agent.Capabilities
		}
		for _, name := range names {
			if !policy.GrantedCapabilities[name] {
				return agentcontract.NewHostError("capability_not_granted")
			}
		}
	}
	return nil
}

func (h *AgentHost) matchWorkflow(run agentcontract.StoredRun, pack, name string, input agentcontract.JSON) (agentcontract.StoredRun, error) {
	state, err := h.Restore(run)
	if err != nil {
		return run, err
	}
	if run.PackID != pack || state.Workflow == nil || state.Workflow.Definition.Name != name {
		return run, agentcontract.NewHostError("request_id_conflict")
	}
	original, err := agentcontract.CanonicalJSON(state.Workflow.Input)
	if err != nil {
		return run, err
	}
	candidate, err := agentcontract.CanonicalJSON(input)
	if err != nil || string(original) != string(candidate) {
		return run, agentcontract.NewHostError("request_id_conflict")
	}
	return run, nil
}

// GetWorkflow returns the authorized business view of an existing workflow.
func (h *AgentHost) GetWorkflow(ctx context.Context, id, owner string) (agentcontract.WorkflowView, error) {
	run, err := h.Get(ctx, id, owner)
	if err != nil {
		return agentcontract.WorkflowView{}, err
	}
	// Progress lives in the checkpoint. Polling does not need Fact bodies.
	raw, err := agentcontract.CanonicalJSON(run.State["runtime"])
	var state agentcontract.RuntimeState
	if err != nil || jsonvalue.DecodeStrict(raw, &state) != nil || state.RunID != run.RunID || state.SchemaVersion != 1 {
		return agentcontract.WorkflowView{}, agentcontract.NewHostError("run_state_invalid")
	}
	if state.Workflow == nil {
		return agentcontract.WorkflowView{}, agentcontract.NewHostError("workflow_unknown")
	}
	w := state.Workflow
	steps, err := jsonvalue.Clone(w.Steps)
	if err != nil {
		return agentcontract.WorkflowView{}, err
	}
	view := agentcontract.WorkflowView{RunID: id, Name: w.Definition.Name, Version: w.Definition.Version, Status: run.Status, Steps: steps, Output: w.Output, Answer: state.AnswerMarkdown, ErrorCode: state.ErrorCode}
	for i := range view.Steps {
		for _, item := range state.Pending {
			if item.Call.CallRef == "wf-"+view.Steps[i].ID {
				view.Steps[i].Status = item.Status
				if item.ErrorCode != nil {
					view.Steps[i].ErrorCode = *item.ErrorCode
				}
			}
		}
		if view.Steps[i].Status != "running" || view.Steps[i].RunID == "" {
			continue
		}
		child, err := h.Get(ctx, view.Steps[i].RunID, owner)
		if err != nil {
			return view, err
		}
		view.Steps[i].Status = child.Status
		if strings.HasPrefix(child.Status, "needs_") {
			view.BlockedRunID, view.BlockedState = child.RunID, child.Status
		}
	}
	if strings.HasPrefix(run.Status, "needs_") {
		view.BlockedRunID, view.BlockedState = run.RunID, run.Status
	}
	return view, nil
}

// ResumeWorkflow reauthorizes and queues a paused workflow for the existing worker.
func (h *AgentHost) ResumeWorkflow(ctx context.Context, id, owner string) (result agentcontract.StoredRun, resultErr error) {
	if _, err := h.GetWorkflow(ctx, id, owner); err != nil {
		return result, err
	}
	run, err := h.Store.Claim(id, owner, h.Settings.LeaseSeconds)
	if err != nil {
		return run, err
	}
	defer func() {
		if err := h.Store.Release(id, owner, run.LeaseToken); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	state, err := h.Restore(run)
	if err != nil {
		return run, err
	}
	if Terminal(run.Status) || run.CancelRequested {
		return run, nil
	}
	if run.Status != "needs_authorization" && run.Status != "waiting" && run.Status != "queued" {
		return run, agentcontract.NewHostError("workflow_resume_invalid")
	}
	if _, err := h.ProjectPolicy(ctx, run); err != nil {
		return run, err
	}
	state.Status, state.ErrorCode = "queued", nil
	return h.save(run, state, "", nil, agentcontract.JSON{"kind": "workflow_resumed"})
}

func (h *AgentHost) workflowParentPolicy(ctx context.Context, run agentcontract.StoredRun, policy agentcontract.ExecutionPolicy) (agentcontract.ExecutionPolicy, error) {
	runtime, _ := run.State["runtime"].(map[string]any)
	if runtime["workflow_parent"] == nil {
		return policy, nil
	}
	var parent agentcontract.WorkflowParent
	value, err := jsonvalue.Clone(runtime["workflow_parent"])
	if err != nil {
		return policy, agentcontract.NewHostError("run_state_invalid")
	}
	raw, err := agentcontract.CanonicalJSON(value)
	if err != nil || jsonvalue.DecodeStrict(raw, &parent) != nil || parent.RunID == run.RunID {
		return policy, agentcontract.NewHostError("run_state_invalid")
	}
	original, err := h.Store.GetRun(parent.RunID, run.OwnerID)
	if err != nil || original.PackID != run.PackID || original.CancelRequested || Terminal(original.Status) {
		return policy, agentcontract.NewHostError("workflow_parent_stopped")
	}
	if parent.StepID != "" {
		allowed := map[string]bool{}
		for _, name := range parent.Capabilities {
			allowed[name] = policy.GrantedCapabilities[name]
		}
		policy.GrantedCapabilities = allowed
	}
	return policy, nil
}

func (h *AgentHost) cancelWorkflowChildren(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState) error {
	return h.cancelWorkflowDescendants(ctx, run, state, 0)
}

func (h *AgentHost) cancelWorkflowDescendants(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, depth int) error {
	if depth > 2 {
		return agentcontract.NewHostError("workflow_checkpoint_invalid")
	}
	ids := []string{}
	if state.Workflow != nil {
		for _, step := range state.Workflow.Definition.Steps {
			if step.Agent != nil {
				ids = append(ids, requestRunID(run.OwnerID, "workflow-agent:"+run.RunID+":"+step.ID))
			}
		}
	}
	for _, item := range state.Pending {
		if item.Call.Capability == "workflows.start" {
			ids = append(ids, requestRunID(run.OwnerID, "workflow:"+item.InvocationID))
		}
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		child, err := h.Store.GetRun(id, run.OwnerID)
		if errors.Is(err, runstore.ErrRunNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		childState, err := h.Restore(child)
		if err != nil {
			return err
		}
		if childState.WorkflowParent == nil || childState.WorkflowParent.RunID != run.RunID {
			return agentcontract.NewHostError("workflow_checkpoint_invalid")
		}
		if !Terminal(child.Status) {
			if _, err := h.Store.RequestCancel(id, run.OwnerID); err != nil {
				return err
			}
		}
		if err := h.cancelWorkflowDescendants(ctx, child, childState, depth+1); err != nil {
			return err
		}
	}
	return nil
}
