package agent

// EffectiveRunConfig records execution limits, not credentials or authorizations.
// Lease renewal and host-wide run slots remain live host operations.
type EffectiveRunConfig struct {
	ModelSelection *ModelSelectionSnapshot `json:"model_selection,omitempty"`
	Model          ModelInfo               `json:"model"`
	Version        int                     `json:"version"`
	Source         string                  `json:"source"`
	Settings       HostSettings            `json:"settings"`
}

// HostOperationLimits reports live host-wide lease and concurrency bounds.
type HostOperationLimits struct {
	LeaseSeconds      float64 `json:"lease_seconds"`
	MaxConcurrentRuns int     `json:"max_concurrent_runs"`
}

// ExecutionCheckpoint describes the saved run revision and any required continuation.
type ExecutionCheckpoint struct {
	Stage     string   `json:"stage"`
	StartedAt float64  `json:"started_at"`
	RetryAt   *float64 `json:"retry_at"`
}

// ExecutionTelemetry reports persisted execution status and active work without inventing a model plan.
type ExecutionTelemetry struct {
	Stage           string             `json:"stage"`
	StartedAt       *float64           `json:"started_at"`
	RetryAt         *float64           `json:"retry_at"`
	WaitReason      *string            `json:"wait_reason"`
	NextWakeAt      *float64           `json:"next_wake_at"`
	CancelRequested bool               `json:"cancel_requested"`
	Active          []ActiveInvocation `json:"active"`
}

// ActiveInvocation exposes the saved identity and status of work still requiring settlement.
type ActiveInvocation struct {
	ID                string   `json:"invocation_id"`
	Capability        string   `json:"capability"`
	Status            string   `json:"status"`
	Attempts          int      `json:"attempts"`
	ApprovalExpiresAt *float64 `json:"approval_expires_at"`
	OperationDeadline *float64 `json:"operation_deadline"`
}

// RuntimeInfo reports the authorized integration's runtime and host limits.
type RuntimeInfo struct {
	Schema              string          `json:"schema"`
	Settings            HostSettings    `json:"settings"`
	Model               ModelInfo       `json:"model"`
	Features            map[string]bool `json:"features"`
	GrantedCapabilities []string        `json:"granted_capabilities"`
}

// ConversationContextSelection records which framework-owned history was selected for a message.
type ConversationContextSelection struct {
	HistoryLimit       int `json:"history_limit"`
	PartCharacterLimit int `json:"part_character_limit"`
	IncludedMessages   int `json:"included_messages"`
	OmittedMessages    int `json:"omitted_messages"`
	TruncatedParts     int `json:"truncated_parts"`
	InputCharacters    int `json:"input_characters"`
}

// ProgressItem is an evidence-derived call status, never a model-authored task plan.
type ProgressItem struct {
	Capability string  `json:"capability"`
	CallRef    string  `json:"call_ref,omitempty"`
	Status     string  `json:"status"`
	FactID     *string `json:"fact_id,omitempty"`
	ErrorCode  *string `json:"error_code,omitempty"`
}

// RunProgress groups persisted call evidence into completed, pending and blocked items.
type RunProgress struct {
	Completed         []ProgressItem `json:"completed,omitempty"`
	Pending           []ProgressItem `json:"pending,omitempty"`
	Blocked           []ProgressItem `json:"blocked,omitempty"`
	CompletedCount    int            `json:"completed_count"`
	BlockedCount      int            `json:"blocked_count"`
	OmittedItems      int            `json:"omitted_items"`
	NoProgressRounds  int            `json:"no_progress_rounds"`
	StagnationWarning bool           `json:"stagnation_warning,omitempty"`
}

// ProgressTracker retains evidence fingerprints used to detect repeated rounds without progress.
type ProgressTracker struct {
	Fingerprint      string   `json:"fingerprint"`
	NoProgressRounds int      `json:"no_progress_rounds"`
	Inspections      []string `json:"inspections,omitempty"`
}

// CounterBudget reports use and remaining allowance for a bounded execution counter.
type CounterBudget struct {
	Used      int64 `json:"used"`
	Limit     int64 `json:"limit"`
	Remaining int64 `json:"remaining"`
}

// TokenBudget reports token use while retaining unknown provider usage as unknown.
type TokenBudget struct {
	Limit           *int64 `json:"limit"`
	Remaining       *int64 `json:"remaining"`
	ReportedTokens  int64  `json:"reported_tokens"`
	EstimatedTokens int64  `json:"estimated_tokens"`
	ReservedTokens  int64  `json:"reserved_tokens"`
	UnknownTokens   int64  `json:"unknown_tokens"`
	ChargedTokens   int64  `json:"charged_tokens"`
}

// RunBudget combines the run's frozen limits with measured execution usage.
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

// RunTelemetry is an owner-scoped evidence view of configuration, budgets and context projection.
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

// DiagnosticFinding explains a saved error using safe framework copy and recovery evidence.
type DiagnosticFinding struct {
	Category   string `json:"category"`
	Code       string `json:"code"`
	Capability string `json:"capability,omitempty"`
	Message    string `json:"message"`
	NextAction string `json:"next_action"`
	Recovered  bool   `json:"recovered"`
	Actionable bool   `json:"actionable"`
}

// RunDiagnostics combines evidence-derived findings, progress and execution budgets.
type RunDiagnostics struct {
	Schema    string              `json:"schema"`
	RunID     string              `json:"run_id"`
	Status    string              `json:"status"`
	Revision  int                 `json:"revision"`
	ElapsedMS int64               `json:"elapsed_ms"`
	Findings  []DiagnosticFinding `json:"findings"`
	Progress  *RunProgress        `json:"progress"`
	Budget    RunBudget           `json:"budget"`
}

// ActionOutcome connects a write in this run to its resulting evidence. It
// deliberately excludes provider operation IDs, argument hashes and raw results:
// private operation bindings must not bypass a capability's ModelOutput policy.
type ActionOutcome struct {
	InvocationID     string `json:"invocation_id"`
	CallRef          string `json:"call_ref,omitempty"`
	Capability       string `json:"capability"`
	ApprovalRequired bool   `json:"approval_required,omitempty"`
	Status           string `json:"status"`
	FactID           string `json:"fact_id,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
	ResultErrorCode  string `json:"result_error_code,omitempty"`
}
