package react

import (
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// RecordModelCall appends one model request metric and updates aggregate usage.
func RecordModelCall(state *agentcontract.RuntimeState, metrics agentcontract.ModelCallMetrics) {
	state.ModelCalls = append(state.ModelCalls, metrics)
	AddModelUsage(&state.ModelUsage, metrics)
}

// RebuildModelUsage reconstructs aggregate usage from the saved request evidence.
func RebuildModelUsage(state *agentcontract.RuntimeState) {
	state.ModelUsage = agentcontract.ModelUsage{}
	for _, metrics := range state.ModelCalls {
		AddModelUsage(&state.ModelUsage, metrics)
	}
}

// AddModelUsage accumulates reported and estimated usage without treating unavailable provider usage as reported zero.
func AddModelUsage(usage *agentcontract.ModelUsage, metrics agentcontract.ModelCallMetrics) {
	usage.Requests += metrics.Attempts
	if metrics.Attempts == 0 {
		return
	}
	usage.ElapsedMilliseconds += metrics.ElapsedMilliseconds
	usage.RetryAttempts += max(0, metrics.Attempts-1)
	if metrics.FormatError != "" || metrics.ErrorCode != nil && *metrics.ErrorCode == "model_decision_invalid" {
		usage.InvalidResponses++
	}
	if metrics.FormatRecovery {
		usage.FormatRecoveryRequests++
	}
	if metrics.UsageAvailable {
		usage.ReportedRequests++
		if metrics.CachedInputTokens != nil {
			addKnownTokens(&usage.CachedInputTokens, *metrics.CachedInputTokens)
			usage.CachedInputRequests++
		}
		if metrics.ReasoningOutputTokens != nil {
			addKnownTokens(&usage.ReasoningOutputTokens, *metrics.ReasoningOutputTokens)
			usage.ReasoningOutputRequests++
		}
		usage.InputTokens += metrics.InputTokens
		usage.OutputTokens += metrics.OutputTokens
		usage.BudgetTokens += metrics.InputTokens + metrics.OutputTokens
		if metrics.Attempts > 1 {
			usage.EstimatedRequests += metrics.Attempts - 1
			usage.BudgetTokens += metrics.EstimatedInputTokens * int64(metrics.Attempts-1)
		}
	} else {
		usage.EstimatedRequests += metrics.Attempts
		usage.BudgetTokens += metrics.EstimatedInputTokens*int64(metrics.Attempts) + metrics.EstimatedOutputTokens
	}
	if metrics.EstimatedCostUSD != nil {
		usage.CostAvailable = true
		usage.PricedRequests++
		usage.EstimatedCostUSD += *metrics.EstimatedCostUSD
	}
}

func addKnownTokens(total **int64, n int64) {
	if *total == nil {
		*total = new(int64)
	}
	**total += n
}
