package agent

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strings"
)

// ModelDecisionError carries a safe model transport or format error code.
type ModelDecisionError struct{ Kind string }

// Error returns the safe error identifier.
func (e ModelDecisionError) Error() string { return e.Kind }

// Code exposes the stable identifier used by framework error handling.
func (e ModelDecisionError) Code() string { return e.Kind }

// ModelErrorCode reduces unknown model errors to the safe model_unavailable code.
// modelErrorCode exposes deliberate codes while keeping uncoded adapter errors private.
func ModelErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	var host *HostError
	if errors.As(err, &host) {
		return host.Code
	}
	var deployment *DeploymentError
	if errors.As(err, &deployment) {
		return deployment.Code
	}
	var registry *RegistryError
	if errors.As(err, &registry) {
		return registry.Code
	}

	// Existing decision and projection validators return these fixed safe strings.
	// Preserve their retry/budget behavior without exposing arbitrary error text.
	switch code := err.Error(); code {
	case "model_decision_invalid", "model_context_measurement_failed", "model_output_reserve_required", "context_too_large":
		return code
	}
	return "model_unavailable"
}

// ModelInfoProvider is optional. Unknown capacities stay nil; no model-name guesses.
type ModelInfoProvider interface{ ModelInfo() ModelInfo }

// ModelInfo describes known model capacities; nil limits mean unknown capacity.
type ModelInfo struct {
	ProtocolReserveTokens int64  `json:"protocol_reserve_tokens"`
	Name                  string `json:"name,omitempty"`
	ContextWindowTokens   *int64 `json:"context_window_tokens"`
	MaxInputTokens        *int64 `json:"max_input_tokens"`
	MaxOutputTokens       *int64 `json:"max_output_tokens"`
}

// InputMeasurement records input token measurement and projection evidence.
type InputMeasurement struct {
	Tokens           int64
	Source           string
	ProjectionReason string
	TargetMet        bool
}

// ModelInputMeasurer optionally measures the same packet and prompt sent to Decide.
// Implementations treat the packet as read-only.
// Unknown uncoded failures become model_unavailable; a coded error exposes its deliberate safe code.
type ModelInputMeasurer interface {
	MeasureInput(ContextPacket, string) (InputMeasurement, error)
}

// KnownTokens returns a positive token count or nil when the count is unknown.
func KnownTokens(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

// DescribeModel reads optional adapter metadata and otherwise supplies default model information.
func DescribeModel(model DecisionModel) ModelInfo {
	if m, ok := model.(ModelInfoProvider); ok {
		return m.ModelInfo()
	}
	return ModelInfo{}
}

// ModelCallMetrics retains request evidence even when a model call fails.
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

// ModelUsage aggregates reported and estimated token use across model requests and purposes.
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

// ModelRequestProgress reports transport attempts and retry timing without upstream response text.
type ModelRequestProgress struct {
	Kind      string  `json:"kind"`
	Attempt   int     `json:"attempt"`
	ErrorCode string  `json:"error_code,omitempty"`
	RetryAt   float64 `json:"retry_at,omitempty"`
}

type modelProgressKey struct{}

// WithModelRequestObserver lets embedded hosts observe retries without replacing
// DecisionModel. Callback failure prevents the next transport attempt.
func WithModelRequestObserver(ctx context.Context, observer func(ModelRequestProgress) error) context.Context {
	return context.WithValue(ctx, modelProgressKey{}, observer)
}

// NotifyModelRequest delivers transport progress to the caller's observer.
// Observer failure prevents the adapter from making its next transport attempt.
func NotifyModelRequest(ctx context.Context, p ModelRequestProgress) error {
	if observer, ok := ctx.Value(modelProgressKey{}).(func(ModelRequestProgress) error); ok {
		return observer(p)
	}
	return nil
}

// ValidDecisionOutputMode checks the supported JSON and tool-call decision output modes.
func ValidDecisionOutputMode(mode string) bool {
	return mode == "" || mode == "json_object" || mode == "output_tools"
}

// ModelConfiguration contains connection references, never resolved API keys.
// Empty purpose selections inherit DefaultProfile.
type ModelConfiguration struct {
	DefaultProfile          string                  `json:"default_profile"`
	DecisionProfile         string                  `json:"decision_profile,omitempty"`
	MemoryExtractionProfile string                  `json:"memory_extraction_profile,omitempty"`
	Profiles                map[string]ModelProfile `json:"profiles"`
}

// ModelProfile configures one model adapter using connection references rather than resolved secrets.
type ModelProfile struct {
	APIType               string       `json:"api_type"`
	DecisionOutputMode    string       `json:"decision_output_mode,omitempty"`
	Model                 string       `json:"model"`
	BaseURL               string       `json:"base_url,omitempty"`
	BaseURLEnv            string       `json:"base_url_env,omitempty"`
	APIKeyRef             string       `json:"api_key_ref"`
	Thinking              string       `json:"thinking,omitempty"`
	ReasoningEffort       string       `json:"reasoning_effort,omitempty"`
	Temperature           *float64     `json:"temperature,omitempty"`
	MaxOutputTokens       int          `json:"max_output_tokens,omitempty"`
	TokenLimitField       string       `json:"token_limit_field,omitempty"`
	ContextWindowTokens   int64        `json:"context_window_tokens,omitempty"`
	MaxInputTokens        int64        `json:"max_input_tokens,omitempty"`
	ProtocolReserveTokens int64        `json:"protocol_reserve_tokens,omitempty"`
	TimeoutSeconds        float64      `json:"timeout_seconds,omitempty"`
	MaxAttempts           int          `json:"max_attempts,omitempty"`
	Prices                *ModelPrices `json:"prices,omitempty"`
}

// ModelPrices holds optional per-million-token prices used only for cost estimates.
type ModelPrices struct {
	InputPerMillion       float64  `json:"input_per_million"`
	OutputPerMillion      float64  `json:"output_per_million"`
	CachedInputPerMillion *float64 `json:"cached_input_per_million,omitempty"`
}

// ModelSelectionSnapshot binds a configuration to a management revision.
type ModelSelectionSnapshot struct {
	Revision int                `json:"revision"`
	Config   ModelConfiguration `json:"config"`
}

// ValidateModelParameters checks that thinking, reasoning and temperature settings are supported by the selected API.
func ValidateModelParameters(api, thinking, effort string, temperature *float64) error {
	if temperature != nil && (math.IsNaN(*temperature) || math.IsInf(*temperature, 0) || *temperature < 0 || *temperature > 2) {
		return NewRegistryError("model_parameters_invalid")
	}
	switch api {
	case "", "compatible_chat":
		if thinking != "" || effort != "" {
			return NewRegistryError("model_parameters_unsupported")
		}
	case "openai_chat":
		if thinking != "" || effort != "" && !ContainsString([]string{"none", "minimal", "low", "medium", "high", "xhigh"}, effort) {
			return NewRegistryError("model_parameters_unsupported")
		}
		if temperature != nil && effort != "" && effort != "none" {
			return NewRegistryError("model_parameters_unsupported")
		}
	case "deepseek_chat":
		if thinking != "" && thinking != "enabled" && thinking != "disabled" || effort != "" && !ContainsString([]string{"low", "high", "max"}, effort) {
			return NewRegistryError("model_parameters_unsupported")
		}
		if thinking == "disabled" && effort != "" || temperature != nil && thinking != "disabled" {
			return NewRegistryError("model_parameters_unsupported")
		}
	default:
		return NewRegistryError("model_api_type_unsupported")
	}
	return nil
}

// ValidModelURL checks a model endpoint URL before deployment resolves its connection.
func ValidModelURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// ProfileID resolves a purpose selection, falling back to the default profile.
func (c ModelConfiguration) ProfileID(purpose string) string {
	id := c.DefaultProfile
	if purpose == "decision" && c.DecisionProfile != "" {
		id = c.DecisionProfile
	}
	if purpose == "memory_extraction" && c.MemoryExtractionProfile != "" {
		id = c.MemoryExtractionProfile
	}
	return id
}

// Validate checks profiles, purpose selections and supported adapter parameters.
func (c ModelConfiguration) Validate() error {
	if len(c.Profiles) < 1 || len(c.Profiles) > 32 {
		return NewRegistryError("model_configuration_invalid")
	}
	for _, id := range []string{c.DefaultProfile, c.ProfileID("decision"), c.ProfileID("memory_extraction")} {
		if _, exists := c.Profiles[id]; !exists {
			return NewRegistryError("model_selection_invalid")
		}
	}
	for id, p := range c.Profiles {
		ref := strings.TrimPrefix(p.APIKeyRef, "secret:")
		if !RegistryID.MatchString(id) || len(p.Model) < 1 || len(p.Model) > 128 || strings.TrimSpace(p.Model) != p.Model || !DeploymentEnvName.MatchString(ref) {
			return NewRegistryError("model_configuration_invalid")
		}
		if (p.BaseURL == "") == (p.BaseURLEnv == "") || p.BaseURL != "" && !ValidModelURL(p.BaseURL) || p.BaseURLEnv != "" && !DeploymentEnvName.MatchString(p.BaseURLEnv) {
			return NewRegistryError("model_endpoint_invalid")
		}
		if err := ValidateModelParameters(p.APIType, p.Thinking, p.ReasoningEffort, p.Temperature); err != nil {
			return err
		}
		if !ValidDecisionOutputMode(p.DecisionOutputMode) {
			return NewRegistryError("model_parameters_invalid")
		}
		if p.TokenLimitField != "" && p.TokenLimitField != "max_tokens" && p.TokenLimitField != "max_completion_tokens" || p.MaxOutputTokens < 0 || p.MaxOutputTokens > 1000000 || p.ContextWindowTokens < 0 || p.ContextWindowTokens > 100000000 || p.MaxInputTokens < 0 || p.MaxInputTokens > 100000000 || p.ProtocolReserveTokens < 0 || p.ProtocolReserveTokens > 1000000 || p.MaxAttempts < 0 || p.MaxAttempts > 10 || math.IsNaN(p.TimeoutSeconds) || math.IsInf(p.TimeoutSeconds, 0) || p.TimeoutSeconds < 0 || p.TimeoutSeconds > 600 {
			return NewRegistryError("model_parameters_invalid")
		}
		if p.Prices != nil {
			values := []float64{p.Prices.InputPerMillion, p.Prices.OutputPerMillion}
			if p.Prices.CachedInputPerMillion != nil {
				values = append(values, *p.Prices.CachedInputPerMillion)
			}
			for _, v := range values {
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
					return NewRegistryError("model_prices_invalid")
				}
			}
		}
	}
	return nil
}

// ModelCheckResult records the outcome and usage of one synthetic profile probe.
type ModelCheckResult struct {
	Profile   string           `json:"profile"`
	Purpose   string           `json:"purpose"`
	Passed    bool             `json:"passed"`
	ErrorCode string           `json:"error_code,omitempty"`
	Metrics   ModelCallMetrics `json:"metrics"`
}
