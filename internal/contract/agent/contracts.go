package agent

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

// JSON represents an object at framework and provider boundaries.
type JSON = map[string]any

// CallRefPattern validates local model call reference identities.
var CallRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// FieldPattern validates request-input field names.
var FieldPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// NewID returns a cryptographically random UUID for a new framework identity.
func NewID() string {
	var b [16]byte

	// Go 1.26's crypto/rand.Read fills the buffer or terminates the process;
	// it never returns a recoverable error.
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// CanonicalJSON encodes finite JSON with stable key ordering and numeric formatting.
// It rejects unsupported and cyclic values; the returned bytes belong to the caller.
func CanonicalJSON(v any) ([]byte, error) {
	return jsonvalue.Canonical(v)
}

// ErrorCode returns a coded error's identifier, or the text of an uncoded error.
// Callers exposing errors across HTTP or model boundaries must select a safe code.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return err.Error()
}

// Fact retains provider evidence and its source, lifetime and model visibility.
// A fact's value is evidence data and cannot grant authorization.
type Fact struct {
	SourcePackID     string       `json:"source_pack_id,omitempty"`
	SourceRelease    string       `json:"source_release,omitempty"`
	SourceSubject    string       `json:"source_subject,omitempty"`
	FactID           string       `json:"fact_id"`
	SourceCapability string       `json:"source_capability"`
	SourceVersion    string       `json:"source_version"`
	Value            JSON         `json:"value"`
	ModelOutput      *ModelOutput `json:"model_output,omitempty"`
	Quality          string       `json:"quality"`
	ObservedAt       time.Time    `json:"observed_at"`
	ReferenceScope   string       `json:"reference_scope"`
	ConnectionID     *string      `json:"connection_id"`
	ExpiresAt        *time.Time   `json:"expires_at"`
}

// FactView carries a bounded model preview and marks omitted paths explicitly.
type FactView struct {
	Fact
	OmittedPaths       [][]any `json:"omitted_paths"`
	ReferenceAvailable bool    `json:"reference_available"`
}

// Observation records a call outcome and references its retained evidence.
type Observation struct {
	CallRef          string  `json:"call_ref"`
	Capability       string  `json:"capability"`
	Status           string  `json:"status"`
	FactID           *string `json:"fact_id"`
	ErrorCode        *string `json:"error_code"`
	Arguments        JSON    `json:"arguments"`
	ArgumentsOmitted bool    `json:"arguments_omitted"`
}

// ContextPacket is the bounded decision input; previews do not replace complete facts.
type ContextPacket struct {
	MaxModelInputTokens     int64             `json:"max_model_input_tokens,omitempty"`
	OriginPackID            string            `json:"origin_pack_id,omitempty"`
	Schema                  string            `json:"schema"`
	Instruction             string            `json:"instruction"`
	Capabilities            []JSON            `json:"capabilities"`
	CapabilityCatalogTotal  int               `json:"capability_catalog_total,omitempty"`
	CapabilitySearchQuery   string            `json:"capability_search_query,omitempty"`
	CapabilitySearchResults []string          `json:"capability_search_results,omitempty"`
	Facts                   []FactView        `json:"facts"`
	Observations            []Observation     `json:"observations"`
	RoundIndex              int               `json:"round_index"`
	RoundsRemaining         int               `json:"rounds_remaining"`
	ToolCallsRemaining      int               `json:"tool_calls_remaining"`
	Skills                  []JSON            `json:"skills"`
	LoadedSkills            map[string]string `json:"loaded_skills"`
	InspectedCapability     JSON              `json:"inspected_capability"`
	InspectedFact           JSON              `json:"inspected_fact"`
	InspectionHistory       []JSON            `json:"inspection_history,omitempty"`
	Followups               []string          `json:"followups"`
	RuntimeFeatures         []string          `json:"runtime_features"`
	ContextOmissions        []string          `json:"context_omissions"`
	Memories                []MemoryView      `json:"memories,omitempty"`
	ModelTokensRemaining    int64             `json:"model_tokens_remaining,omitempty"`
	MaxModelOutputTokens    int               `json:"max_model_output_tokens,omitempty"`
	Progress                *RunProgress      `json:"progress,omitempty"`
	ActionOutcomes          []ActionOutcome   `json:"action_outcomes,omitempty"`
	CompletionReview        *Decision         `json:"completion_review,omitempty"`
}

// ToolCall binds a model call reference to a capability and JSON arguments.
type ToolCall struct {
	CallRef    string `json:"call_ref"`
	Capability string `json:"capability"`
	Arguments  JSON   `json:"arguments"`
	Reason     string `json:"reason"`
}

// Decision represents one typed action in the ReAct protocol.
// ModelCall is local request telemetry and is excluded from the decision JSON.
type Decision struct {
	Schema         string                `json:"schema"`
	Kind           string                `json:"kind"`
	Calls          []ToolCall            `json:"calls,omitempty"`
	AnswerMarkdown string                `json:"answer_markdown,omitempty"`
	FactIDs        []string              `json:"fact_ids,omitempty"`
	ResultRefs     []ResultRefRequest    `json:"result_refs,omitempty"`
	Field          string                `json:"field,omitempty"`
	Prompt         string                `json:"prompt,omitempty"`
	InputSchema    *RequestedInputSchema `json:"input_schema,omitempty"`
	Query          string                `json:"query,omitempty"`
	Name           string                `json:"name,omitempty"`
	FactID         string                `json:"fact_id,omitempty"`
	Path           []any                 `json:"path,omitempty"`
	ModelCall      *ModelCallMetrics     `json:"-"`
}

// DecisionTooManyCallsError rejects a batch larger than the protocol permits.
type DecisionTooManyCallsError struct{}

// Error returns the safe error identifier.
func (DecisionTooManyCallsError) Error() string {
	return "model_decision_invalid: tool_batch.calls has at most 4 items"
}

// Code exposes the stable identifier used by framework error handling.
func (DecisionTooManyCallsError) Code() string { return "model_decision_invalid" }

// Validate checks the decision kind, field bounds and JSON serializability.
func (d Decision) Validate() error {
	if d.Schema != "" && d.Schema != "agenstra.decision.v1" {
		return errors.New("model_decision_invalid")
	}
	switch d.Kind {
	case "tool_batch":
		if len(d.Calls) > 4 {
			return DecisionTooManyCallsError{}
		}
		if len(d.Calls) < 1 {
			return errors.New("model_decision_invalid")
		}
		for _, c := range d.Calls {
			if !CallRefPattern.MatchString(c.CallRef) || len(c.Capability) < 1 || len(c.Capability) > 330 || len(c.Reason) < 1 || len(c.Reason) > 500 || c.Arguments == nil {
				return errors.New("model_decision_invalid")
			}
		}
	case "final":
		if len(d.AnswerMarkdown) < 1 || len(d.AnswerMarkdown) > 30000 || len(d.FactIDs) > 50 || len(d.ResultRefs) > 20 {
			return errors.New("model_decision_invalid")
		}
		for _, id := range d.FactIDs {
			if !ValidUUID(id) {
				return errors.New("model_decision_invalid")
			}
		}
		for _, ref := range d.ResultRefs {
			if !ValidResultRefRequest(ref) {
				return errors.New("model_decision_invalid")
			}
		}
	case "request_input":
		if !FieldPattern.MatchString(d.Field) || len(d.Prompt) < 1 || len(d.Prompt) > 1000 {
			return errors.New("model_decision_invalid")
		}
		if d.InputSchema != nil && d.InputSchema.Validate() != nil {
			return errors.New("model_decision_invalid")
		}
	case "search_capabilities":
		if len(strings.TrimSpace(d.Query)) < 1 || len([]rune(d.Query)) > 300 {
			return errors.New("model_decision_invalid")
		}
	case "read_skill", "inspect_capability":
		if len(d.Name) < 1 || len(d.Name) > 330 {
			return errors.New("model_decision_invalid")
		}
	case "inspect_fact":
		if !ValidUUID(d.FactID) || len(d.Path) > 16 {
			return errors.New("model_decision_invalid")
		}
		for _, p := range d.Path {
			switch x := p.(type) {
			case string:
				_ = x
			case json.Number:
				i, err := x.Int64()
				if err != nil || i < 0 {
					return errors.New("model_decision_invalid")
				}
			case float64:
				if x < 0 || x != math.Trunc(x) {
					return errors.New("model_decision_invalid")
				}
			case int:
				if x < 0 {
					return errors.New("model_decision_invalid")
				}
			default:
				return errors.New("model_decision_invalid")
			}
		}
	default:
		return errors.New("model_decision_invalid")
	}
	if _, err := json.Marshal(d); err != nil {
		return errors.New("model_decision_invalid")
	}
	return nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidUUID checks the framework UUID format.
func ValidUUID(s string) bool { return uuidPattern.MatchString(s) }

// StrictDecision decodes exactly one decision object and validates the decision protocol.
func StrictDecision(raw []byte) (Decision, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Decision{}, errors.New("model_decision_invalid")
	}
	var d Decision
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&d); err != nil {
		return d, errors.New("model_decision_invalid")
	}
	if _, ok := obj["schema"]; !ok {
		d.Schema = "agenstra.decision.v1"
	}
	allowed := map[string]map[string]bool{"tool_batch": {"schema": true, "kind": true, "calls": true}, "final": {"schema": true, "kind": true, "answer_markdown": true, "fact_ids": true, "result_refs": true}, "request_input": {"schema": true, "kind": true, "field": true, "prompt": true, "input_schema": true}, "search_capabilities": {"schema": true, "kind": true, "query": true}, "read_skill": {"schema": true, "kind": true, "name": true}, "inspect_capability": {"schema": true, "kind": true, "name": true}, "inspect_fact": {"schema": true, "kind": true, "fact_id": true, "path": true}}
	keys := allowed[d.Kind]
	if keys == nil {
		return d, errors.New("model_decision_invalid")
	}
	for k := range obj {
		if !keys[k] {
			return d, errors.New("model_decision_invalid")
		}
	}
	if d.Kind == "tool_batch" {
		var calls []map[string]json.RawMessage
		if err := json.Unmarshal(obj["calls"], &calls); err != nil {
			return d, errors.New("model_decision_invalid")
		}
		for _, c := range calls {
			for k := range c {
				if k != "call_ref" && k != "capability" && k != "arguments" && k != "reason" {
					return d, errors.New("model_decision_invalid")
				}
			}
		}
	}
	return d, d.Validate()
}

// OperationReceipt retains an external job identity and its polling state.
type OperationReceipt struct {
	OperationID   string           `json:"operation_id"`
	Binding       OperationBinding `json:"binding"`
	PollArguments JSON             `json:"poll_arguments"`
	NextPollAt    float64          `json:"next_poll_at"`
	Deadline      float64          `json:"deadline"`
	Polls         int              `json:"polls"`
}

// Invocation journals the original call identity, parameters and execution evidence.
// Retries and reconciliation retain this identity rather than creating another write.
type Invocation struct {
	InvocationID      string             `json:"invocation_id"`
	Call              ToolCall           `json:"call"`
	OriginalArguments JSON               `json:"original_arguments"`
	ArgumentsSHA256   string             `json:"arguments_sha256"`
	Status            string             `json:"status"`
	ApprovedUntil     *float64           `json:"approved_until"`
	ApprovalExpiresAt *float64           `json:"approval_expires_at"`
	ApprovedHash      *string            `json:"approved_hash"`
	Attempts          int                `json:"attempts"`
	ErrorCode         *string            `json:"error_code"`
	FactID            *string            `json:"fact_id"`
	Operation         *OperationReceipt  `json:"operation"`
	PollInFlight      bool               `json:"poll_in_flight"`
	Reconciled        bool               `json:"reconciled,omitempty"`
	Receipt           *InvocationReceipt `json:"receipt,omitempty"`
}

// InvocationReceipt records the provider's outcome independently of orchestration
// and result-storage errors. Success confirms this call, not an entire task or
// the completion of an asynchronous business operation.
type InvocationReceipt struct {
	InvocationID    string `json:"invocation_id"`
	Capability      string `json:"capability"`
	Effect          string `json:"effect"`
	ArgumentsSHA256 string `json:"arguments_sha256"`
	Status          string `json:"status"`
	ErrorCode       string `json:"error_code,omitempty"`
	FactID          string `json:"fact_id,omitempty"`
	ResultSHA256    string `json:"result_sha256,omitempty"`
	ResultBytes     int    `json:"result_bytes,omitempty"`
	ResultErrorCode string `json:"result_error_code,omitempty"`
	OperationID     string `json:"operation_id,omitempty"`
	OperationStatus string `json:"operation_status,omitempty"`
	Reconciled      bool   `json:"reconciled,omitempty"`
}

// RuntimeState is the complete checkpoint for a single run.
// A driver owns its mutable state; model input is a separate bounded projection.
type RuntimeState struct {
	InvocationReceipts      []InvocationReceipt   `json:"invocation_receipts,omitempty"`
	Execution               *ExecutionCheckpoint  `json:"execution,omitempty"`
	ContextPolicy           *ContextPolicy        `json:"context_policy,omitempty"`
	ContextPolicyCursor     int                   `json:"context_policy_cursor,omitempty"`
	ContextTelemetry        *ContextTelemetry     `json:"context_telemetry,omitempty"`
	SchemaVersion           int                   `json:"schema_version"`
	RunID                   string                `json:"run_id"`
	Instruction             string                `json:"instruction"`
	Status                  string                `json:"status"`
	Facts                   []Fact                `json:"facts"`
	Observations            []Observation         `json:"observations"`
	ModelObservations       []Observation         `json:"model_observations"`
	Decisions               []JSON                `json:"decisions"`
	UsedRefs                []string              `json:"used_refs"`
	Repeated                map[string]int        `json:"repeated"`
	LoadedSkills            []string              `json:"loaded_skills"`
	CapabilitySearchQuery   string                `json:"capability_search_query,omitempty"`
	CapabilitySearchResults []string              `json:"capability_search_results,omitempty"`
	Followups               []string              `json:"followups"`
	InspectedCapability     *string               `json:"inspected_capability"`
	InspectedFact           JSON                  `json:"inspected_fact"`
	RoundsUsed              int                   `json:"rounds_used"`
	ToolCallsUsed           int                   `json:"tool_calls_used"`
	PollCallsUsed           int                   `json:"poll_calls_used"`
	Pending                 []Invocation          `json:"pending"`
	AnswerMarkdown          string                `json:"answer_markdown"`
	ResultRefs              []ResultObjectRef     `json:"result_refs,omitempty"`
	ErrorCode               *string               `json:"error_code"`
	InputField              *string               `json:"input_field"`
	InputPrompt             *string               `json:"input_prompt"`
	InputSchema             *RequestedInputSchema `json:"input_schema,omitempty"`
	ModelCalls              []ModelCallMetrics    `json:"model_calls,omitempty"`
	ModelUsage              ModelUsage            `json:"model_usage"`
	Progress                *ProgressTracker      `json:"progress_tracker,omitempty"`
	SteeringCursor          int                   `json:"steering_cursor,omitempty"`
}

// RunResult exposes transient execution status, evidence and any requested user action.
type RunResult struct {
	ContextTelemetry *ContextTelemetry     `json:"context_telemetry,omitempty"`
	Progress         *RunProgress          `json:"progress,omitempty"`
	Status           string                `json:"status"`
	AnswerMarkdown   string                `json:"answer_markdown"`
	ResultRefs       []ResultObjectRef     `json:"result_refs,omitempty"`
	ErrorCode        *string               `json:"error_code"`
	InputField       *string               `json:"input_field"`
	InputPrompt      *string               `json:"input_prompt"`
	InputSchema      *RequestedInputSchema `json:"input_schema,omitempty"`
	Facts            []Fact                `json:"facts"`
	Observations     []Observation         `json:"observations"`
	Decisions        []JSON                `json:"decisions"`
	ModelCalls       []ModelCallMetrics    `json:"model_calls,omitempty"`
	ModelUsage       ModelUsage            `json:"model_usage"`
}

// Strptr returns a pointer to an independent string value.
func Strptr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// ValueAt reads an object or array path from JSON and reports invalid or missing steps.
func ValueAt(value any, path []any) (any, error) {
	cur := value
	for _, p := range path {
		switch v := cur.(type) {
		case map[string]any:
			key, ok := p.(string)
			if !ok {
				return nil, errors.New("invalid")
			}
			next, exists := v[key]
			if !exists {
				return nil, errors.New("invalid")
			}
			cur = next
		case []any:
			i, ok := jsonvalue.Index(p)
			if !ok || i < 0 || i >= len(v) {
				return nil, errors.New("invalid")
			}
			cur = v[i]
		default:
			return nil, errors.New("invalid")
		}
	}
	return cur, nil
}

// StoredRun is an owner-scoped snapshot with revision and lease fencing metadata.
// Timestamps and lease deadlines use Unix seconds.
type StoredRun struct {
	RunID           string         `json:"run_id"`
	OwnerID         string         `json:"owner_id"`
	PackID          string         `json:"pack_id"`
	Status          string         `json:"status"`
	State           map[string]any `json:"state"`
	Revision        int            `json:"revision"`
	NextWakeAt      *float64       `json:"next_wake_at"`
	LeaseToken      string         `json:"lease_token"`
	LeaseUntil      *float64       `json:"lease_until"`
	CreatedAt       float64        `json:"created_at"`
	UpdatedAt       float64        `json:"updated_at"`
	CancelRequested bool           `json:"cancel_requested"`
}

// DecodeObject decodes persisted object JSON using the framework numeric representation.
func DecodeObject(data string) (map[string]any, error) {
	var result map[string]any
	d := json.NewDecoder(bytes.NewBufferString(data))
	d.UseNumber()
	if err := d.Decode(&result); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("stored JSON must be an object")
	}
	return result, nil
}

// EncodeDocument stores one finite canonical JSON document.
func EncodeDocument(v any) (string, error) { b, e := CanonicalJSON(v); return string(b), e }

// DecodeDocument reads one complete JSON document while preserving numeric values.
// Struct decoding accepts extension fields used by existing persisted records.
func DecodeDocument(raw string, v any) error {
	if !json.Valid([]byte(raw)) {
		return errors.New("invalid JSON document")
	}
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.UseNumber()
	return d.Decode(v)
}
