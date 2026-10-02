package agenstra

import "context"

// UsageAvailable distinguishes provider-reported usage from an estimate.
// EstimatedCostUSD is populated only when the caller configured model prices.
type ModelCallMetrics struct {
	FormatRecovery        bool     `json:"format_recovery,omitempty"`
	Profile               string   `json:"profile,omitempty"`
	Model                 string   `json:"model,omitempty"`
	ResponseModel         string   `json:"response_model,omitempty"`
	APIType               string   `json:"api_type,omitempty"`
	Thinking              string   `json:"thinking,omitempty"`
	ReasoningEffort       string   `json:"reasoning_effort,omitempty"`
	CachedInputTokens     *int64   `json:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens *int64   `json:"reasoning_output_tokens,omitempty"`
	FormatError           string   `json:"format_error,omitempty"`
	Purpose               string   `json:"purpose,omitempty"`
	SourceID              string   `json:"source_id,omitempty"`
	Reservation           bool     `json:"reservation,omitempty"`
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
	ReportedRequests        int     `json:"reported_requests"`
	FormatRecoveryRequests  int     `json:"format_recovery_requests"`
	CachedInputTokens       *int64  `json:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens   *int64  `json:"reasoning_output_tokens,omitempty"`
	CachedInputRequests     int     `json:"cached_input_requests"`
	ReasoningOutputRequests int     `json:"reasoning_output_requests"`
	PricedRequests          int     `json:"priced_requests"`
	ElapsedMilliseconds     int64   `json:"elapsed_ms"`
	InvalidResponses        int     `json:"invalid_responses"`
	RetryAttempts           int     `json:"retry_attempts"`
	Requests                int     `json:"requests"`
	InputTokens             int64   `json:"input_tokens"`
	OutputTokens            int64   `json:"output_tokens"`
	BudgetTokens            int64   `json:"budget_tokens"`
	EstimatedRequests       int     `json:"estimated_requests"`
	EstimatedCostUSD        float64 `json:"estimated_cost_usd"`
	CostAvailable           bool    `json:"cost_available"`
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
