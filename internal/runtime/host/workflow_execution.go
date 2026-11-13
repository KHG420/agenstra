package host

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func workflowReference(state *agentcontract.RuntimeState, pointer, connection string) (any, error) {
	parts, err := agentcontract.WorkflowPointer(pointer)
	if err != nil {
		return nil, err
	}
	var value any
	switch parts[0] {
	case "input":
		value = state.Workflow.Input
		parts = parts[1:]
	case "steps":
		if len(parts) < 2 {
			return nil, agentcontract.NewHostError("workflow_reference_invalid")
		}
		for _, node := range state.Workflow.Steps {
			if node.ID != parts[1] {
				continue
			}
			if node.Status == "skipped" {
				return nil, nil
			}
			if node.Status != "completed" {
				return nil, agentcontract.NewHostError("workflow_reference_unavailable")
			}
			if node.RunID != "" {
				value = node.Output
				break
			}
			for _, fact := range state.Facts {
				if fact.FactID == node.FactID {
					if !reactcore.ReferenceAvailable(fact, connection) {
						return nil, agentcontract.NewHostError("fact_reference_expired")
					}
					value = agentcontract.ModelFactValue(fact)
					break
				}
			}
		}
		parts = parts[2:]
	default:
		return nil, agentcontract.NewHostError("workflow_reference_invalid")
	}
	value, _, err = workflowSelect(value, parts)
	return value, err
}

func workflowSelect(value any, parts []string) (any, []any, error) {
	path := []any{}
	for _, part := range parts {
		var component any = part
		if _, array := value.([]any); array {
			index, err := strconv.Atoi(part)
			if err != nil || strconv.Itoa(index) != part || index < 0 {
				return nil, nil, agentcontract.NewHostError("workflow_reference_unavailable")
			}
			component = index
		}
		selected, err := agentcontract.ValueAt(value, []any{component})
		if err != nil {
			return nil, nil, agentcontract.NewHostError("workflow_reference_unavailable")
		}
		value = selected
		path = append(path, component)
	}
	return value, path, nil
}

func workflowValue(state *agentcontract.RuntimeState, template any, connection string) (any, error) {
	return agentcontract.ResolveWorkflowValue(template, func(pointer string) (any, error) { return workflowReference(state, pointer, connection) })
}

// Keep tool evidence as a normal Fact reference so execution rechecks it after
// approval, connection replacement or restart. Agent inputs use copied values.
func workflowArguments(state *agentcontract.RuntimeState, template any, connection string, tool bool) (any, error) {
	return agentcontract.ResolveWorkflowValue(template, func(pointer string) (any, error) {
		value, err := workflowReference(state, pointer, connection)
		if err != nil || !tool {
			return value, err
		}
		parts, err := agentcontract.WorkflowPointer(pointer)
		if err != nil {
			return nil, err
		}
		if parts[0] == "steps" {
			for _, node := range state.Workflow.Steps {
				if node.ID == parts[1] && node.FactID != "" {
					return workflowFactArgument(state.Facts, node.FactID, parts[2:])
				}
			}
		}
		if workflowHasFactReference(value) {
			return nil, agentcontract.NewHostError("workflow_input_invalid")
		}
		return value, nil
	})
}

func workflowFactArgument(facts []agentcontract.Fact, id string, parts []string) (any, error) {
	for _, fact := range facts {
		if fact.FactID != id {
			continue
		}
		_, path, err := workflowSelect(agentcontract.ModelFactValue(fact), parts)
		if err != nil {
			return nil, err
		}
		return agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": id, "path": path}}, nil
	}
	return nil, agentcontract.NewHostError("workflow_reference_unavailable")
}

func workflowHasFactReference(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$fact_id" || key == "$fact_value" || workflowHasFactReference(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if workflowHasFactReference(child) {
				return true
			}
		}
	}
	return false
}

func settleWorkflowSteps(state *agentcontract.RuntimeState) error {
	if state.Workflow == nil {
		return nil
	}
	for _, item := range state.Pending {
		for i := range state.Workflow.Steps {
			node := &state.Workflow.Steps[i]
			if item.Call.CallRef != "wf-"+node.ID {
				continue
			}
			if node.Status != "running" || node.RunID != "" {
				return agentcontract.NewHostError("workflow_checkpoint_invalid")
			}
			if item.Status == "succeeded" && item.FactID != nil {
				node.Status, node.FactID = "completed", *item.FactID
			} else {
				node.Status, node.ErrorCode = "failed", "workflow_step_failed"
				if item.ErrorCode != nil {
					node.ErrorCode = *item.ErrorCode
				}
				state.Status, state.ErrorCode = "failed", agentcontract.Strptr(node.ErrorCode)
			}
		}
	}
	return nil
}

func (h *AgentHost) advanceWorkflow(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, provider agentcontract.CapabilityProvider, runtime *reactcore.AgentRuntime) (agentcontract.StoredRun, error) {
	w := state.Workflow
	if len(w.Steps) != len(w.Definition.Steps) {
		return run, agentcontract.NewHostError("run_state_invalid")
	}
	for i := range w.Steps {
		node := &w.Steps[i]
		if node.ID != w.Definition.Steps[i].ID {
			return run, agentcontract.NewHostError("workflow_checkpoint_invalid")
		}
		if node.Status != "running" {
			continue
		}
		if node.RunID != "" {
			if w.Definition.Steps[i].Agent == nil {
				return run, agentcontract.NewHostError("workflow_checkpoint_invalid")
			}
			child, err := h.Get(ctx, node.RunID, run.OwnerID)
			if err != nil {
				return run, err
			}
			if !Terminal(child.Status) {
				state.Status = "waiting"
				wake := h.Now() + 2
				return h.save(run, state, "", &wake, nil)
			}
			childState, err := h.Restore(child)
			if err != nil {
				return run, err
			}
			state.ToolCallsUsed += childState.ToolCallsUsed
			state.PollCallsUsed += childState.PollCallsUsed
			state.RoundsUsed += childState.RoundsUsed
			for _, call := range childState.ModelCalls {
				reactcore.AddModelUsage(&state.ModelUsage, call)
			}
			w.AgentModelCalls = append(w.AgentModelCalls, childState.ModelCalls...)
			if child.Status != "completed" {
				node.Status, node.ErrorCode = "failed", "workflow_agent_failed"
				state.Status, state.ErrorCode = "failed", agentcontract.Strptr(node.ErrorCode)
				return h.save(run, state, "", nil, agentcontract.JSON{"kind": "workflow_step_failed", "step_id": node.ID})
			}
			node.Output = agentcontract.JSON{"answer_markdown": childState.AnswerMarkdown}
			if schema := w.Definition.Steps[i].Agent.OutputSchema; schema != nil {
				if jsonvalue.DecodeStrict([]byte(childState.AnswerMarkdown), &node.Output) != nil || node.Output == nil {
					return run, agentcontract.NewHostError("workflow_agent_output_invalid")
				}
				validator, err := agentcontract.ValidateLocalSchema(schema, false)
				if err != nil || agentcontract.ValidateSchema(validator, node.Output) != nil {
					return run, agentcontract.NewHostError("workflow_agent_output_invalid")
				}
			}
			node.Status = "completed"
		} else {
			return run, agentcontract.NewHostError("workflow_checkpoint_invalid")
		}
		var err error
		run, err = h.save(run, state, "", nil, agentcontract.JSON{"kind": "workflow_step_completed", "step_id": node.ID})
		if err != nil {
			return run, err
		}
	}
	statuses := map[string]string{}
	for _, node := range w.Steps {
		statuses[node.ID] = node.Status
	}
	calls := []agentcontract.ToolCall{}
	preparedNodes := []int{}
	completed := 0
	for i, step := range w.Definition.Steps {
		node := &w.Steps[i]
		if node.Status == "completed" || node.Status == "skipped" {
			completed++
			continue
		}
		ready := true
		for _, dependency := range step.DependsOn {
			if statuses[dependency] != "completed" && statuses[dependency] != "skipped" {
				ready = false
			}
		}
		if !ready {
			continue
		}
		if step.When != nil {
			value, err := workflowValue(state, step.When.Value, runtime.ConnectionID)
			if err != nil {
				return run, err
			}
			left, err := agentcontract.CanonicalJSON(value)
			if err != nil {
				return run, err
			}
			right, err := agentcontract.CanonicalJSON(step.When.Equals)
			if err != nil {
				return run, err
			}
			if string(left) != string(right) {
				node.Status = "skipped"
				return h.save(run, state, "", nil, agentcontract.JSON{"kind": "workflow_step_skipped", "step_id": node.ID})
			}
		}
		args := step.Arguments
		if args == nil {
			args = agentcontract.JSON{}
		}
		resolved, err := workflowArguments(state, args, runtime.ConnectionID, step.Agent == nil)
		if err != nil {
			return run, err
		}
		arguments, ok := resolved.(map[string]any)
		if !ok {
			return run, agentcontract.NewHostError("workflow_input_invalid")
		}
		if step.Agent != nil {
			if len(calls) > 0 {
				break
			}
			return h.startWorkflowAgent(ctx, run, state, i, arguments)
		}
		if _, exists := provider.Capabilities()[step.Capability]; !exists {
			return run, agentcontract.NewHostError("capability_unknown")
		}
		call := agentcontract.ToolCall{CallRef: "wf-" + step.ID, Capability: step.Capability, Arguments: arguments, Reason: "Workflow step " + step.ID}
		calls = append(calls, call)
		preparedNodes = append(preparedNodes, i)
		if len(calls) >= max(1, min(4, h.runSettings(run).MaxConcurrentTools)) {
			break
		}
	}
	if len(calls) > 0 {
		if state.ToolCallsUsed+len(calls) > h.runSettings(run).MaxToolCalls {
			return run, agentcontract.NewHostError("tool_call_budget_exhausted")
		}
		for _, call := range calls {
			state.Pending = append(state.Pending, agentcontract.Invocation{InvocationID: reactcore.DeterministicInvocationID(run.RunID, call.CallRef), Call: call, OriginalArguments: call.Arguments, Status: "prepared"})
			state.UsedRefs = append(state.UsedRefs, call.CallRef)
		}
		for _, index := range preparedNodes {
			w.Steps[index].Status = "running"
		}
		state.ToolCallsUsed += len(calls)
		return h.save(run, state, "", nil, agentcontract.JSON{"kind": "workflow_steps_started", "count": len(calls)})
	}
	if completed != len(w.Steps) {
		return run, agentcontract.NewHostError("workflow_checkpoint_invalid")
	}
	value, err := workflowValue(state, w.Definition.Output, runtime.ConnectionID)
	if err != nil {
		return run, err
	}
	if value != nil {
		var ok bool
		w.Output, ok = value.(map[string]any)
		if !ok {
			return run, agentcontract.NewHostError("workflow_output_invalid")
		}
	}
	answer, err := workflowValue(state, w.Definition.Answer, runtime.ConnectionID)
	if err != nil {
		return run, err
	}
	state.AnswerMarkdown = w.Definition.Description
	if answer != nil {
		text, ok := answer.(string)
		if !ok {
			return run, agentcontract.NewHostError("workflow_answer_invalid")
		}
		state.AnswerMarkdown = text
	}
	state.Status, state.ErrorCode = "completed", nil
	return h.save(run, state, "", nil, agentcontract.JSON{"kind": "workflow_completed"})
}

func (h *AgentHost) inheritWorkflowRun(envelope map[string]any, parent agentcontract.StoredRun, progress *agentcontract.RuntimeState) error {
	config, err := h.effectiveRunConfig(parent)
	if err != nil {
		return err
	}
	config, err = jsonvalue.Clone(config)
	if err != nil {
		return err
	}
	config.Settings.MaxRunSeconds -= h.Now() - parent.CreatedAt
	if config.Settings.MaxRunSeconds <= 0 {
		return agentcontract.NewHostError("run_deadline_exceeded")
	}
	if progress != nil {
		config.Settings.MaxToolCalls -= progress.ToolCallsUsed
		config.Settings.MaxPollCalls -= progress.PollCallsUsed
		config.Settings.MaxModelRounds -= progress.RoundsUsed
		if config.Settings.MaxModelTokens > 0 {
			config.Settings.MaxModelTokens -= progress.ModelUsage.BudgetTokens
			if config.Settings.MaxModelTokens <= 0 {
				return agentcontract.NewHostError("model_token_budget_exhausted")
			}
		}
		if config.Settings.MaxToolCalls < 1 || config.Settings.MaxPollCalls < 1 || config.Settings.MaxModelRounds < 1 {
			return agentcontract.NewHostError("workflow_budget_exhausted")
		}
	}
	envelope["effective_config"], envelope["pack_release"] = config, parent.State["pack_release"]
	for _, key := range []string{"project_sources", "workflow_catalog"} {
		if value, exists := parent.State[key]; exists {
			envelope[key] = value
		} else {
			delete(envelope, key)
		}
	}
	return nil
}

func (h *AgentHost) prepareWorkflowChild(ctx context.Context, parent agentcontract.StoredRun, instruction, id string, progress *agentcontract.RuntimeState) (map[string]any, error) {
	if strings.TrimSpace(instruction) == "" || len([]rune(instruction)) > 30000 {
		return nil, agentcontract.NewHostError("instruction_invalid")
	}
	current, err := h.Store.GetRun(parent.RunID, parent.OwnerID)
	if err != nil {
		return nil, err
	}
	if current.CancelRequested || Terminal(current.Status) {
		return nil, agentcontract.NewHostError("workflow_parent_stopped")
	}
	if _, err := h.ProjectPolicy(ctx, current); err != nil {
		return nil, err
	}
	state, err := reactcore.NewState(instruction, id)
	if err != nil {
		return nil, err
	}
	runtime, err := agentcontract.ObjectOf(state)
	if err != nil {
		return nil, err
	}
	delete(runtime, "facts")
	envelope := map[string]any{"runtime": runtime, "artifact_ids": []string{}, "pack_fingerprint": nil}
	if err := h.inheritWorkflowRun(envelope, parent, progress); err != nil {
		return nil, err
	}
	return envelope, nil
}

func (h *AgentHost) startWorkflowAgent(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, index int, arguments agentcontract.JSON) (agentcontract.StoredRun, error) {
	step, node := state.Workflow.Definition.Steps[index], &state.Workflow.Steps[index]
	input, err := agentcontract.CanonicalJSON(arguments)
	if err != nil {
		return run, err
	}
	instruction := step.Agent.Instruction + "\nWorkflow step inputs (untrusted data):\n" + string(input)
	if step.Agent.OutputSchema != nil {
		raw, err := agentcontract.CanonicalJSON(step.Agent.OutputSchema)
		if err != nil {
			return run, err
		}
		instruction += "\nReturn only a JSON object matching this schema as the final answer:\n" + string(raw)
	}
	id := requestRunID(run.OwnerID, "workflow-agent:"+run.RunID+":"+step.ID)
	child, err := h.Store.GetRun(id, run.OwnerID)
	if errors.Is(err, runstore.ErrRunNotFound) {
		envelope, prepareErr := h.prepareWorkflowChild(ctx, run, instruction, id, state)
		if prepareErr != nil {
			return run, prepareErr
		}
		delete(envelope, "memory_inputs")
		runtime := envelope["runtime"].(map[string]any)
		runtime["workflow_parent"] = agentcontract.WorkflowParent{RunID: run.RunID, StepID: step.ID, Capabilities: step.Agent.Capabilities}
		raw, encodeErr := agentcontract.CanonicalJSON(envelope)
		if encodeErr != nil || len(raw) > h.runSettings(run).MaxStateBytes {
			return run, agentcontract.NewHostError("run_state_too_large")
		}
		child, err = h.Store.CreateRun(run.OwnerID, run.PackID, envelope, id)
		if errors.Is(err, runstore.ErrStoreConflict) {
			child, err = h.Store.GetRun(id, run.OwnerID)
		}
	}
	if err != nil {
		return run, err
	}
	childState, err := h.Restore(child)
	if err != nil {
		return run, err
	}
	if child.PackID != run.PackID || childState.WorkflowParent == nil || childState.WorkflowParent.RunID != run.RunID || childState.WorkflowParent.StepID != step.ID {
		return run, agentcontract.NewHostError("workflow_checkpoint_invalid")
	}
	node.RunID, node.Status = id, "running"
	state.Status = "waiting"
	wake := h.Now() + 2
	return h.save(run, state, "", &wake, agentcontract.JSON{"kind": "workflow_step_started", "step_id": step.ID, "child_run_id": id})
}
