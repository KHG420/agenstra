package agent

import (
	"math"
)

// ContextPolicy sets optional thresholds for projecting model input within existing budgets.
type ContextPolicy struct {
	TriggerRatio float64 `json:"trigger_ratio"`
	TargetRatio  float64 `json:"target_ratio"`
}

// Validate checks projection ratios without changing execution limits.
func (p ContextPolicy) Validate() error {
	if math.IsNaN(p.TriggerRatio) || math.IsNaN(p.TargetRatio) || math.IsInf(p.TriggerRatio, 0) || math.IsInf(p.TargetRatio, 0) || p.TriggerRatio < 0 || p.TriggerRatio > 1 || p.TargetRatio < 0 || p.TargetRatio > p.TriggerRatio || (p.TriggerRatio > 0 && p.TargetRatio == 0) {
		return NewHostError("context_policy_invalid")
	}
	return nil
}

// ContextTelemetry describes the final input projection, outside model context.
// Tokens are added only by a model measurement; character counts are exact.
type ContextTelemetry struct {
	Policy                   ContextPolicy         `json:"policy"`
	Strategy                 string                `json:"strategy"`
	PolicyUnit               string                `json:"policy_unit"`
	ProjectionReason         string                `json:"projection_reason"`
	TargetMet                bool                  `json:"target_met"`
	InputTokens              *int64                `json:"input_tokens"`
	ReportedInputTokens      *int64                `json:"reported_input_tokens"`
	TokenMeasurementSource   string                `json:"token_measurement_source,omitempty"`
	ModelContextWindowTokens *int64                `json:"model_context_window_tokens"`
	EffectiveInputTokenLimit *int64                `json:"effective_input_token_limit"`
	ReservedOutputTokens     *int64                `json:"reserved_output_tokens"`
	TokensRemaining          *int64                `json:"tokens_remaining"`
	TokenUtilization         *float64              `json:"token_utilization"`
	Schema                   string                `json:"schema"`
	ProjectionID             string                `json:"projection_id"`
	Round                    int                   `json:"round"`
	MeasuredAt               float64               `json:"measured_at"`
	InputCharacters          int                   `json:"input_characters"`
	CharacterLimit           int                   `json:"character_limit"`
	CharactersRemaining      int                   `json:"characters_remaining"`
	Utilization              float64               `json:"character_utilization"`
	OverLimit                bool                  `json:"over_limit"`
	CandidateCharacters      int                   `json:"candidate_characters"`
	Components               map[string]int        `json:"components"`
	Omissions                ContextOmissionCounts `json:"omissions"`
}

// ContextOmissionCounts records how much context was excluded from the model projection.
type ContextOmissionCounts struct {
	Observations    int `json:"observations"`
	Arguments       int `json:"arguments"`
	FactPaths       int `json:"fact_paths"`
	Skills          int `json:"skills"`
	Memories        int `json:"memories"`
	DeferredSchemas int `json:"deferred_schemas"`
}
