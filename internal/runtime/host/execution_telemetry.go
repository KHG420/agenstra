package host

import (
	"context"
	"sort"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func (h *AgentHost) recordExecution(state *agentcontract.RuntimeState, event agentcontract.JSON) {
	if event == nil {
		return
	}
	kind, _ := event["kind"].(string)
	stage := ""
	switch kind {
	case "model_requested", "model_retry_started":
		stage = "model_decision"
	case "memory_requested":
		stage = "memory_extraction"
	case "model_retry_wait":
		stage = "retry_wait"
	case "call_started", "operation_poll_started":
		stage = "tool_execution"
	case "run_resumed", "model_decided", "memory_decided", "call_finished", "parallel_finished":
		stage = "planning"
	}
	if state.Status != "running" {
		stage = state.Status
	}
	if kind == "model_retry_started" && event["purpose"] == "memory_extraction" {
		stage = "memory_extraction"
	}
	if stage == "" {
		return
	}
	if state.Execution == nil || state.Execution.Stage != stage {
		state.Execution = &agentcontract.ExecutionCheckpoint{Stage: stage, StartedAt: h.Now()}
	}
	state.Execution.RetryAt = nil
	if retryAt, ok := event["retry_at"].(float64); ok && retryAt > 0 {
		state.Execution.RetryAt = &retryAt
	}
}

func executionTelemetry(state agentcontract.RuntimeState, run agentcontract.StoredRun) agentcontract.ExecutionTelemetry {
	e := agentcontract.ExecutionTelemetry{Stage: run.Status, NextWakeAt: run.NextWakeAt, CancelRequested: run.CancelRequested, Active: []agentcontract.ActiveInvocation{}}
	if state.Execution != nil {
		n := state.Execution.StartedAt
		if state.Execution.Stage == run.Status || run.Status == "running" {
			e.StartedAt = &n
		}
		if run.Status == "running" {
			e.Stage = state.Execution.Stage
			e.RetryAt = state.Execution.RetryAt
		}
	}
	if run.Status == "waiting" || run.Status == "needs_input" || run.Status == "needs_approval" || run.Status == "needs_authorization" || run.Status == "needs_reconciliation" {
		reason := run.Status
		if state.ErrorCode != nil {
			reason = *state.ErrorCode
		}
		e.WaitReason = &reason
	}
	for _, item := range state.Pending {
		if item.Status == "succeeded" || item.Status == "failed" {
			continue
		}
		a := agentcontract.ActiveInvocation{ID: item.InvocationID, Capability: item.Call.Capability, Status: item.Status, Attempts: item.Attempts, ApprovalExpiresAt: item.ApprovalExpiresAt}
		if item.Operation != nil {
			n := item.Operation.Deadline
			a.OperationDeadline = &n
		}
		e.Active = append(e.Active, a)
	}
	return e
}

// GetRuntimeInfo reads authorized host limits without calling a model or business capability.
func (h *AgentHost) GetRuntimeInfo(ctx context.Context, owner, pack string) (agentcontract.RuntimeInfo, error) {
	p, err := h.Policy(ctx, owner, pack, false)
	if err != nil {
		return agentcontract.RuntimeInfo{}, err
	}
	names := []string{}
	for name, granted := range p.GrantedCapabilities {
		if granted {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	_, extract := h.Model.(agentcontract.MemoryExtractor)
	return agentcontract.RuntimeInfo{Schema: "agenstra.runtime-info.v1", Settings: NormalizedRunSettings(h.Settings), Model: agentcontract.DescribeModel(h.Model), Features: map[string]bool{"telemetry": true, "context_policy": p.AllowModelData, "steering": p.AllowModelData, "memory_extraction": extract, "reconciliation": h.Reconciler != nil, "pause": false, "single_step": false, "content_stream": false}, GrantedCapabilities: names}, nil
}
