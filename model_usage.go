package agenstra

import "context"

// UsageAvailable distinguishes provider-reported usage from an estimate.
// EstimatedCostUSD is populated only when the caller configured model prices.
type ModelCallMetrics struct {
	Round                 int      `json:"round"`
	Attempts              int      `json:"attempts"`
	InputTokens           int64    `json:"input_tokens"`
	OutputTokens          int64    `json:"output_tokens"`
	UsageAvailable        bool     `json:"usage_available"`
	EstimatedInputTokens  int64    `json:"estimated_input_tokens"`
	EstimatedOutputTokens int64    `json:"estimated_output_tokens"`
	EstimatedCostUSD      *float64 `json:"estimated_cost_usd,omitempty"`
	ElapsedMilliseconds   int64    `json:"elapsed_ms"`
	FinishReason          string   `json:"finish_reason,omitempty"`
	RequestID             string   `json:"request_id,omitempty"`
	ErrorCode             *string  `json:"error_code,omitempty"`
}

type ModelUsage struct {
	Requests          int     `json:"requests"`
	InputTokens       int64   `json:"input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	BudgetTokens      int64   `json:"budget_tokens"`
	EstimatedRequests int     `json:"estimated_requests"`
	EstimatedCostUSD  float64 `json:"estimated_cost_usd"`
	CostAvailable     bool    `json:"cost_available"`
}

type modelMetricsKey struct{}
type modelTokenBudgetKey struct{}

func packetTokenBudget(ctx context.Context) int64 {
	remaining, _ := ctx.Value(modelTokenBudgetKey{}).(int64)
	return remaining
}
func modelMetrics(ctx context.Context) *ModelCallMetrics {
	m, _ := ctx.Value(modelMetricsKey{}).(*ModelCallMetrics)
	return m
}

func recordModelCall(state *RuntimeState, metrics ModelCallMetrics) {
	state.ModelCalls = append(state.ModelCalls, metrics)
	addModelUsage(&state.ModelUsage, metrics)
}

func rebuildModelUsage(state *RuntimeState) {
	state.ModelUsage = ModelUsage{}
	for _, metrics := range state.ModelCalls {
		addModelUsage(&state.ModelUsage, metrics)
	}
}

func addModelUsage(usage *ModelUsage, metrics ModelCallMetrics) {
	usage.Requests += metrics.Attempts
	if metrics.Attempts == 0 {
		return
	}
	if metrics.UsageAvailable {
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
		usage.EstimatedCostUSD += *metrics.EstimatedCostUSD
	}
}
