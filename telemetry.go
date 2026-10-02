package agenstra

import (
	"context"
	"encoding/json"
)

type CounterBudget struct {
	Used      int64 `json:"used"`
	Limit     int64 `json:"limit"`
	Remaining int64 `json:"remaining"`
}
type TokenBudget struct {
	Limit           *int64 `json:"limit"`
	Remaining       *int64 `json:"remaining"`
	ReportedTokens  int64  `json:"reported_tokens"`
	EstimatedTokens int64  `json:"estimated_tokens"`
	ReservedTokens  int64  `json:"reserved_tokens"`
	UnknownTokens   int64  `json:"unknown_tokens"`
	ChargedTokens   int64  `json:"charged_tokens"`
}
type RunBudget struct {
	ModelRounds      CounterBudget         `json:"model_rounds"`
	ToolCalls        CounterBudget         `json:"tool_calls"`
	PollCalls        CounterBudget         `json:"poll_calls"`
	Tokens           TokenBudget           `json:"tokens"`
	Usage            ModelUsage            `json:"usage"`
	UsageByPurpose   map[string]ModelUsage `json:"usage_by_purpose"`
	Deadline         float64               `json:"deadline"`
	SecondsRemaining float64               `json:"seconds_remaining"`
}
type RunTelemetry struct {
	Execution       ExecutionTelemetry  `json:"execution"`
	HostOperations  HostOperationLimits `json:"host_operations"`
	ContextPolicy   ContextPolicy       `json:"context_policy"`
	Schema          string              `json:"schema"`
	RunID           string              `json:"run_id"`
	RunRevision     int                 `json:"run_revision"`
	ObservedAt      float64             `json:"observed_at"`
	Context         *ContextTelemetry   `json:"context"`
	EffectiveConfig EffectiveRunConfig  `json:"effective_config"`
	Budget          RunBudget           `json:"budget"`
}

func countBudget(used, limit int) CounterBudget {
	return CounterBudget{int64(used), int64(limit), int64(max(0, limit-used))}
}

func (h *AgentHost) telemetry(run StoredRun) (RunTelemetry, error) {
	c, err := h.effectiveRunConfig(run)
	if err != nil {
		return RunTelemetry{}, err
	}
	raw, err := CanonicalJSON(run.State["runtime"])
	if err != nil {
		return RunTelemetry{}, err
	}
	var state RuntimeState
	if err = json.Unmarshal(raw, &state); err != nil || state.RunID != run.RunID {
		return RunTelemetry{}, hostError("run_state_invalid")
	}
	s := c.Settings
	now := h.now()
	b := RunBudget{ModelRounds: countBudget(state.RoundsUsed, s.MaxModelRounds), ToolCalls: countBudget(state.ToolCallsUsed, s.MaxToolCalls), PollCalls: countBudget(state.PollCallsUsed, s.MaxPollCalls), Usage: state.ModelUsage, UsageByPurpose: map[string]ModelUsage{}, Deadline: run.CreatedAt + s.MaxRunSeconds}
	b.SecondsRemaining = max(0, b.Deadline-now)
	for i, call := range state.ModelCalls {
		purpose := call.Purpose
		if purpose == "" {
			purpose = "decision"
		}
		u := b.UsageByPurpose[purpose]
		addModelUsage(&u, call)
		b.UsageByPurpose[purpose] = u
		var charged ModelUsage
		addModelUsage(&charged, call)
		if call.Reservation || call.ErrorCode != nil && *call.ErrorCode == "model_outcome_unknown" {
			if i == len(state.ModelCalls)-1 && run.Status == "running" && run.LeaseUntil != nil && *run.LeaseUntil > now {
				b.Tokens.ReservedTokens += charged.BudgetTokens
			} else {
				b.Tokens.UnknownTokens += charged.BudgetTokens
			}
		} else {
			reported := int64(0)
			if call.UsageAvailable {
				reported = call.InputTokens + call.OutputTokens
			}
			b.Tokens.ReportedTokens += reported
			b.Tokens.EstimatedTokens += max(int64(0), charged.BudgetTokens-reported)
		}
	}
	b.Tokens.ChargedTokens = b.Tokens.ReportedTokens + b.Tokens.EstimatedTokens + b.Tokens.ReservedTokens + b.Tokens.UnknownTokens
	if s.MaxModelTokens > 0 {
		limit := s.MaxModelTokens
		remaining := max(int64(0), limit-b.Tokens.ChargedTokens)
		b.Tokens.Limit = &limit
		b.Tokens.Remaining = &remaining
	}
	policy := c.Settings.ContextPolicy
	if state.ContextPolicy != nil {
		policy = *state.ContextPolicy
	}
	return RunTelemetry{Execution: executionTelemetry(state, run), HostOperations: HostOperationLimits{LeaseSeconds: normalizedRunSettings(h.Settings).LeaseSeconds, MaxConcurrentRuns: normalizedRunSettings(h.Settings).MaxConcurrentRuns}, ContextPolicy: policy, Schema: "agenstra.run-telemetry.v1", RunID: run.RunID, RunRevision: run.Revision, ObservedAt: now, Context: state.ContextTelemetry, EffectiveConfig: c, Budget: b}, nil
}

// GetTelemetry reads persisted measurements. It does not construct a Provider,
// restore Fact bodies, or invoke a model.
func (h *AgentHost) GetTelemetry(ctx context.Context, id, owner string) (RunTelemetry, error) {
	run, err := h.Get(ctx, id, owner)
	if err != nil {
		return RunTelemetry{}, err
	}
	return h.telemetry(run)
}
