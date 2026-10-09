package agenstra

import (
	"github.com/KHG420/agenstra/internal/runtime/engine"
)

// CompletionContext is supplied only after citation identity checks succeed.
// Validators must be read-only and respect cancellation. Facts are complete,
// rather than the bounded previews supplied to the model.
type CompletionContext = engine.CompletionContext

// CompletionValidator checks cited business evidence after citation identities are verified.
// It must respect cancellation and return deliberate feedback through CompletionValidationError.
type CompletionValidator = engine.CompletionValidator

// CompletionValidationError carries deliberately model-visible feedback.
// Ordinary errors are reduced to a stable code and never expose their text.
type CompletionValidationError = engine.CompletionValidationError

// FactRequirement constrains the latest cited value from one capability.
type FactRequirement = engine.FactRequirement

// RequireFactValues checks the latest evidence from each required capability,
// its citation and a JSON value. JSON Schema equality preserves numeric semantics.
// A host can compose this validator with domain-specific answer validation.
func RequireFactValues(requirements ...FactRequirement) (CompletionValidator, error) {
	return engine.RequireFactValues(requirements...)
}

// ActionOutcome connects a write in this run to its resulting evidence. It
// deliberately excludes provider operation IDs, argument hashes and raw results:
// private operation bindings must not bypass a capability's ModelOutput policy.
type ActionOutcome = engine.ActionOutcome

// ContextPolicy sets optional thresholds for projecting model input within existing budgets.
type ContextPolicy = engine.ContextPolicy

// ContextTelemetry describes the final input projection, outside model context.
// Tokens are added only by a model measurement; character counts are exact.
type ContextTelemetry = engine.ContextTelemetry

// ContextOmissionCounts records how much context was excluded from the model projection.
type ContextOmissionCounts = engine.ContextOmissionCounts

// JSON represents an object at framework and provider boundaries.
type JSON = engine.JSON

// NewID returns a cryptographically random UUID for a new framework identity.
func NewID() string { return engine.NewID() }

// CanonicalJSON encodes finite JSON with stable key ordering and numeric formatting.
// It rejects unsupported and cyclic values; the returned bytes belong to the caller.
func CanonicalJSON(v any) ([]byte, error) { return engine.CanonicalJSON(v) }

// ErrorCode returns a coded error's identifier, or the text of an uncoded error.
// Callers exposing errors across HTTP or model boundaries must select a safe code.
func ErrorCode(err error) string { return engine.ErrorCode(err) }

// Fact retains provider evidence and its source, lifetime and model visibility.
// A fact's value is evidence data and cannot grant authorization.
type Fact = engine.Fact

// FactView carries a bounded model preview and marks omitted paths explicitly.
type FactView = engine.FactView

// Observation records a call outcome and references its retained evidence.
type Observation = engine.Observation

// ContextPacket is the bounded decision input; previews do not replace complete facts.
type ContextPacket = engine.ContextPacket

// ToolCall binds a model call reference to a capability and JSON arguments.
type ToolCall = engine.ToolCall

// Decision represents one typed action in the ReAct protocol.
// ModelCall is local request telemetry and is excluded from the decision JSON.
type Decision = engine.Decision

// DecisionTooManyCallsError rejects a batch larger than the protocol permits.
type DecisionTooManyCallsError = engine.DecisionTooManyCallsError

// OperationReceipt retains an external job identity and its polling state.
type OperationReceipt = engine.OperationReceipt

// Invocation journals the original call identity, parameters and execution evidence.
// Retries and reconciliation retain this identity rather than creating another write.
type Invocation = engine.Invocation

// InvocationReceipt records the provider's outcome independently of orchestration
// and result-storage errors. Success confirms this call, not an entire task or
// the completion of an asynchronous business operation.
type InvocationReceipt = engine.InvocationReceipt

// RuntimeState is the complete checkpoint for a single run.
// A driver owns its mutable state; model input is a separate bounded projection.
type RuntimeState = engine.RuntimeState

// RunResult exposes transient execution status, evidence and any requested user action.
type RunResult = engine.RunResult

// HostOperationLimits reports live host-wide lease and concurrency bounds.
type HostOperationLimits = engine.HostOperationLimits

// ExecutionCheckpoint describes the saved run revision and any required continuation.
type ExecutionCheckpoint = engine.ExecutionCheckpoint

// ExecutionTelemetry reports persisted execution status and active work without inventing a model plan.
type ExecutionTelemetry = engine.ExecutionTelemetry

// ActiveInvocation exposes the saved identity and status of work still requiring settlement.
type ActiveInvocation = engine.ActiveInvocation

// RuntimeInfo reports the authorized integration's runtime and host limits.
type RuntimeInfo = engine.RuntimeInfo

// ConversationContextSelection records which framework-owned history was selected for a message.
type ConversationContextSelection = engine.ConversationContextSelection

// RequestedInputSchema constrains a single text answer without changing the
// existing SupplyInput(field, text, revision) API.
type RequestedInputSchema = engine.RequestedInputSchema

// ValidateRequestedInput checks a user answer against the saved input request.
// A nil schema retains the existing unconstrained text behavior.
func ValidateRequestedInput(schema *RequestedInputSchema, text string) error {
	return engine.ValidateRequestedInput(schema, text)
}

// ProgressItem is an evidence-derived call status, never a model-authored task plan.
type ProgressItem = engine.ProgressItem

// RunProgress groups persisted call evidence into completed, pending and blocked items.
type RunProgress = engine.RunProgress

// ProgressTracker retains evidence fingerprints used to detect repeated rounds without progress.
type ProgressTracker = engine.ProgressTracker

// RunSource is an explicit delegation for one other host project's pack.
// Capabilities are local names in that pack. The originating user's connection
// must also delegate them, and the target must verify that user's permissions.
type RunSource = engine.RunSource

// ResultRefRequest names evidence; only the server resolves its business ID.
type ResultRefRequest = engine.ResultRefRequest

// ResultObjectRef binds an external result identity to a cited fact and declared path.
type ResultObjectRef = engine.ResultObjectRef

// EffectiveRunConfig records execution limits, not credentials or authorizations.
// Lease renewal and host-wide run slots remain live host operations.
type EffectiveRunConfig = engine.EffectiveRunConfig

// AgentRuntime owns decision budgets, references and a run's mutable execution state.
// A runtime is used by one driver at a time; its provider and model remain caller-owned.
type AgentRuntime = engine.AgentRuntime

// ReferenceAvailable checks expiry and the connection scope of evidence references.
func ReferenceAvailable(f Fact, connectionID string) bool {
	return engine.ReferenceAvailable(f, connectionID)
}

// ResolveArgument copies JSON arguments and resolves permitted references from complete facts.
func ResolveArgument(value any, facts map[string]Fact, connectionID string, check bool) (any, error) {
	return engine.ResolveArgument(value, facts, connectionID, check)
}

// Reject records a failed model observation without dispatching a provider call.
func Reject(state *RuntimeState, callRef, capability, code string, args map[string]any, factID string) {
	engine.Reject(state, callRef, capability, code, args, factID)
}

// NewState creates a checkpoint using the runtime's default limits.
func NewState(instruction, runID string) (*RuntimeState, error) {
	return engine.NewState(instruction, runID)
}
