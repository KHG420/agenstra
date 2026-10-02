package agenstra

import (
	"context"
	"sort"
)

type HostOperationLimits struct {
	LeaseSeconds      float64 `json:"lease_seconds"`
	MaxConcurrentRuns int     `json:"max_concurrent_runs"`
}

type ExecutionCheckpoint struct {
	Stage     string   `json:"stage"`
	StartedAt float64  `json:"started_at"`
	RetryAt   *float64 `json:"retry_at"`
}
type ExecutionTelemetry struct {
	Stage           string             `json:"stage"`
	StartedAt       *float64           `json:"started_at"`
	RetryAt         *float64           `json:"retry_at"`
	WaitReason      *string            `json:"wait_reason"`
	NextWakeAt      *float64           `json:"next_wake_at"`
	CancelRequested bool               `json:"cancel_requested"`
	Active          []ActiveInvocation `json:"active"`
}
type ActiveInvocation struct {
	ID                string   `json:"invocation_id"`
	Capability        string   `json:"capability"`
	Status            string   `json:"status"`
	Attempts          int      `json:"attempts"`
	ApprovalExpiresAt *float64 `json:"approval_expires_at"`
	OperationDeadline *float64 `json:"operation_deadline"`
}

func (h *AgentHost) recordExecution(state *RuntimeState, event JSON) {
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
		state.Execution = &ExecutionCheckpoint{Stage: stage, StartedAt: h.now()}
	}
	state.Execution.RetryAt = nil
	if retryAt, ok := event["retry_at"].(float64); ok && retryAt > 0 {
		state.Execution.RetryAt = &retryAt
	}
}
func executionTelemetry(state RuntimeState, run StoredRun) ExecutionTelemetry {
	e := ExecutionTelemetry{Stage: run.Status, NextWakeAt: run.NextWakeAt, CancelRequested: run.CancelRequested, Active: []ActiveInvocation{}}
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
		a := ActiveInvocation{ID: item.InvocationID, Capability: item.Call.Capability, Status: item.Status, Attempts: item.Attempts, ApprovalExpiresAt: item.ApprovalExpiresAt}
		if item.Operation != nil {
			n := item.Operation.Deadline
			a.OperationDeadline = &n
		}
		e.Active = append(e.Active, a)
	}
	return e
}

type RuntimeInfo struct {
	Schema              string          `json:"schema"`
	Settings            HostSettings    `json:"settings"`
	Model               ModelInfo       `json:"model"`
	Features            map[string]bool `json:"features"`
	GrantedCapabilities []string        `json:"granted_capabilities"`
}

func (h *AgentHost) GetRuntimeInfo(ctx context.Context, owner, pack string) (RuntimeInfo, error) {
	p, err := h.policy(ctx, owner, pack, false)
	if err != nil {
		return RuntimeInfo{}, err
	}
	names := []string{}
	for name, granted := range p.GrantedCapabilities {
		if granted {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	_, extract := h.Model.(MemoryExtractor)
	return RuntimeInfo{Schema: "agenstra.runtime-info.v1", Settings: normalizedRunSettings(h.Settings), Model: modelInfo(h.Model), Features: map[string]bool{"telemetry": true, "context_policy": p.AllowModelData, "steering": p.AllowModelData, "memory_extraction": extract, "reconciliation": h.Reconciler != nil, "pause": false, "single_step": false, "content_stream": false}, GrantedCapabilities: names}, nil
}

type ConversationContextSelection struct {
	HistoryLimit       int `json:"history_limit"`
	PartCharacterLimit int `json:"part_character_limit"`
	IncludedMessages   int `json:"included_messages"`
	OmittedMessages    int `json:"omitted_messages"`
	TruncatedParts     int `json:"truncated_parts"`
	InputCharacters    int `json:"input_characters"`
}
