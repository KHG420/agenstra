package host

import (
	"context"
	"encoding/json"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func countBudget(used, limit int) agentcontract.CounterBudget {
	return agentcontract.CounterBudget{Used: int64(used), Limit: int64(limit), Remaining: int64(max(0, limit-used))}
}

// Telemetry projects evidence and budgets from a supplied saved run; HTTP callers must first obtain it through the owner-authorized host API.
func (h *AgentHost) Telemetry(run agentcontract.StoredRun) (agentcontract.RunTelemetry, error) {
	c, err := h.effectiveRunConfig(run)
	if err != nil {
		return agentcontract.RunTelemetry{}, err
	}
	raw, err := agentcontract.CanonicalJSON(run.State["runtime"])
	if err != nil {
		return agentcontract.RunTelemetry{}, err
	}
	var state agentcontract.RuntimeState
	if err = json.Unmarshal(raw, &state); err != nil || state.RunID != run.RunID {
		return agentcontract.RunTelemetry{}, agentcontract.NewHostError("run_state_invalid")
	}
	s := c.Settings
	now := h.Now()
	b := agentcontract.RunBudget{ModelRounds: countBudget(state.RoundsUsed, s.MaxModelRounds), ToolCalls: countBudget(state.ToolCallsUsed, s.MaxToolCalls), PollCalls: countBudget(state.PollCallsUsed, s.MaxPollCalls), Usage: state.ModelUsage, UsageByPurpose: map[string]agentcontract.ModelUsage{}, Deadline: run.CreatedAt + s.MaxRunSeconds}
	b.SecondsRemaining = max(0, b.Deadline-now)
	for i, call := range state.ModelCalls {
		purpose := call.Purpose
		if purpose == "" {
			purpose = "decision"
		}
		u := b.UsageByPurpose[purpose]
		reactcore.AddModelUsage(&u, call)
		b.UsageByPurpose[purpose] = u
		var charged agentcontract.ModelUsage
		reactcore.AddModelUsage(&charged, call)
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
	return agentcontract.RunTelemetry{Execution: executionTelemetry(state, run), HostOperations: agentcontract.HostOperationLimits{LeaseSeconds: NormalizedRunSettings(h.Settings).LeaseSeconds, MaxConcurrentRuns: NormalizedRunSettings(h.Settings).MaxConcurrentRuns}, ContextPolicy: policy, Schema: "agenstra.run-telemetry.v1", RunID: run.RunID, RunRevision: run.Revision, ObservedAt: now, Context: state.ContextTelemetry, EffectiveConfig: c, Budget: b}, nil
}

// GetTelemetry reads persisted measurements. It does not construct a Provider,
// restore Fact bodies, or invoke a model.
func (h *AgentHost) GetTelemetry(ctx context.Context, id, owner string) (agentcontract.RunTelemetry, error) {
	run, err := h.Get(ctx, id, owner)
	if err != nil {
		return agentcontract.RunTelemetry{}, err
	}
	return h.Telemetry(run)
}
