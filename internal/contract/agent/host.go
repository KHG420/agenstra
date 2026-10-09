package agent

import (
	"context"
	"errors"
)

// HostError carries a stable run lifecycle or authorization error code.
type HostError struct{ Code string }

// Error returns the safe error identifier.
func (e *HostError) Error() string { return e.Code }

// NewHostError constructs a safe host lifecycle or authorization error.
func NewHostError(code string) error { return &HostError{Code: code} }

// ExecutionPolicy is the live authorization resolved from trusted host state.
// Its maps must not be changed while a caller is using the policy.
type ExecutionPolicy struct {
	GrantedCapabilities  map[string]bool     `json:"granted_capabilities"`
	ApprovalCapabilities map[string]bool     `json:"approval_capabilities"`
	AllowModelData       bool                `json:"allow_model_data"`
	Subject              string              `json:"subject,omitempty"`
	PermissionsVerified  bool                `json:"permissions_verified,omitempty"`
	Delegations          map[string][]string `json:"delegations,omitempty"`
}

// HostSettings bounds execution, persistence and model context.
// Configure settings before the host begins serving concurrent runs.
type HostSettings struct {
	MaxConversationHistoryMessages   int           `json:"max_conversation_history_messages,omitempty"`
	MaxConversationHistoryCharacters int           `json:"max_conversation_history_characters,omitempty"`
	MaxConversationMessages          int           `json:"max_conversation_messages,omitempty"`
	ContextPolicy                    ContextPolicy `json:"context_policy"`
	ModelContextWindowTokens         int64         `json:"model_context_window_tokens,omitempty"`
	MaxModelInputTokens              int64         `json:"max_model_input_tokens,omitempty"`
	ModelOutputReserveTokens         int           `json:"model_output_reserve_tokens,omitempty"`
	ModelProtocolReserveTokens       int64         `json:"model_protocol_reserve_tokens,omitempty"`
	LeaseSeconds                     float64       `json:"lease_seconds"`
	MaxModelRounds                   int           `json:"max_model_rounds"`
	MaxToolCalls                     int           `json:"max_tool_calls"`
	MaxPollCalls                     int           `json:"max_poll_calls"`
	MaxRunSeconds                    float64       `json:"max_run_seconds"`
	MaxContextCharacters             int           `json:"max_context_characters"`
	MaxContextCapabilities           int           `json:"max_context_capabilities,omitempty"`
	MaxArtifactBytes                 int           `json:"max_artifact_bytes"`
	MaxActiveArtifactBytes           int           `json:"max_active_artifact_bytes"`
	MaxStateBytes                    int           `json:"max_state_bytes"`
	ModelTimeoutSeconds              float64       `json:"model_timeout_seconds"`
	InvocationTimeoutSeconds         float64       `json:"invocation_timeout_seconds"`
	MaxInvocationAttempts            int           `json:"max_invocation_attempts"`
	RetryIntervalSeconds             float64       `json:"retry_interval_seconds"`
	ApprovalSeconds                  float64       `json:"approval_seconds"`
	MaxConcurrentRuns                int           `json:"max_concurrent_runs"`
	MaxModelTokens                   int64         `json:"max_model_tokens,omitempty"`
	MaxModelOutputTokens             int           `json:"max_model_output_tokens,omitempty"`
	ModelTokenLimitField             string        `json:"model_token_limit_field,omitempty"`
	MaxStagnantRounds                int           `json:"max_stagnant_rounds,omitempty"`
	MaxConcurrentTools               int           `json:"max_concurrent_tools,omitempty"`
}

// DefaultHostSettings returns independent default execution limits.
func DefaultHostSettings() HostSettings {
	return HostSettings{MaxConversationHistoryMessages: 6, MaxConversationHistoryCharacters: 1500, MaxConversationMessages: 500, LeaseSeconds: 60, MaxModelRounds: 30, MaxToolCalls: 80, MaxPollCalls: 720, MaxRunSeconds: 86400, MaxContextCharacters: 80000, MaxArtifactBytes: 8000000, MaxActiveArtifactBytes: 64000000, MaxStateBytes: 8000000, ModelTimeoutSeconds: 60, InvocationTimeoutSeconds: 300, MaxInvocationAttempts: 3, RetryIntervalSeconds: 5, ApprovalSeconds: 900, MaxConcurrentRuns: 4, MaxStagnantRounds: 8, MaxConcurrentTools: 4}
}

// Validate rejects execution limits outside the supported ranges.
func (s HostSettings) Validate() error {
	if s.MaxConversationHistoryMessages < 0 || s.MaxConversationHistoryMessages > 100 || s.MaxConversationHistoryCharacters < 0 || s.MaxConversationHistoryCharacters > 12000 || s.MaxConversationMessages < 0 || s.MaxConversationMessages > 500 {
		return errors.New("host_settings_invalid")
	}
	if err := s.ContextPolicy.Validate(); err != nil {
		return err
	}
	if s.ModelContextWindowTokens < 0 || s.ModelContextWindowTokens > 100000000 || s.MaxModelInputTokens < 0 || s.MaxModelInputTokens > 100000000 || s.ModelOutputReserveTokens < 0 || s.ModelOutputReserveTokens > 1000000 || s.ModelProtocolReserveTokens < 0 || s.ModelProtocolReserveTokens > 1000000 {
		return errors.New("host_settings_invalid")
	}
	if s.MaxConcurrentTools < 0 || s.MaxConcurrentTools > 4 {
		return errors.New("host_settings_invalid")
	}
	if s.MaxContextCapabilities < 0 || s.MaxContextCapabilities > 200 {
		return errors.New("host_settings_invalid")
	}
	if s.MaxStagnantRounds < 0 || s.MaxStagnantRounds > 1000 {
		return errors.New("host_settings_invalid")
	}
	if s.MaxModelTokens < 0 || s.MaxModelTokens > 100000000 || s.MaxModelOutputTokens < 0 || s.MaxModelOutputTokens > 1000000 || (s.ModelTokenLimitField != "" && s.ModelTokenLimitField != "max_tokens" && s.ModelTokenLimitField != "max_completion_tokens") {
		return errors.New("host_settings_invalid")
	}
	if s.LeaseSeconds < 3 || s.LeaseSeconds > 3600 || s.MaxModelRounds < 1 || s.MaxModelRounds > 1000 || s.MaxToolCalls < 1 || s.MaxToolCalls > 10000 || s.MaxPollCalls < 1 || s.MaxPollCalls > 100000 || s.MaxRunSeconds <= 0 || s.MaxContextCharacters < 1000 || s.MaxArtifactBytes < 1024 || s.MaxActiveArtifactBytes < 1024 || s.MaxStateBytes < 1024 || s.ModelTimeoutSeconds <= 0 || s.InvocationTimeoutSeconds <= 0 || s.MaxInvocationAttempts < 1 || s.MaxInvocationAttempts > 10 || s.RetryIntervalSeconds < 1 || s.ApprovalSeconds < 1 || s.ApprovalSeconds > 86400 || s.MaxConcurrentRuns < 1 || s.MaxConcurrentRuns > 64 {
		return errors.New("host_settings_invalid")
	}
	return nil
}

// ProviderFactory opens an owner-specific capability connection for a pack.
// The host closes each successfully opened provider.
type ProviderFactory func(context.Context, string, string) (CapabilityProvider, error)

// PolicyResolver reads current authorization; cached model data cannot replace it.
type PolicyResolver func(context.Context, string, string) (ExecutionPolicy, error)

// ReleaseResolver selects the immutable pack release bound to a new run.
type ReleaseResolver func(context.Context, string, string) (string, error)

// ReleaseProviderFactory opens the pinned release for an existing run.
// The host closes each successfully opened provider.
type ReleaseProviderFactory func(context.Context, string, string, string) (CapabilityProvider, error)

// ObjectOf round-trips a value into a detached JSON object using the framework numeric decoder.
func ObjectOf(v any) (map[string]any, error) {
	b, e := CanonicalJSON(v)
	if e != nil {
		return nil, e
	}
	return DecodeObject(string(b))
}

// ContainsString reports exact membership in a string collection.
func ContainsString(items []string, value string) bool {
	for _, v := range items {
		if v == value {
			return true
		}
	}
	return false
}
