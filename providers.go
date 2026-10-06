package agenstra

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/jsonvalue"
)

// InvocationContext carries the trusted identity and stable idempotency key of a call.
// Project routing may fill target identity fields before a provider invocation.
type InvocationContext struct {
	RunID          string `json:"run_id"`
	InvocationID   string `json:"invocation_id"`
	IdempotencyKey string `json:"idempotency_key"`
	OwnerID        string `json:"owner_id"`
	ConnectionID   string `json:"connection_id"`
	OriginPackID   string `json:"origin_pack_id,omitempty"`
	TargetPackID   string `json:"target_pack_id,omitempty"`
	TargetSubject  string `json:"target_subject,omitempty"`
	TargetRelease  string `json:"target_release,omitempty"`
}

// OperationBinding declares how to read and poll an external asynchronous receipt.
type OperationBinding struct {
	IDPath               []any    `json:"id_path"`
	StatusPath           []any    `json:"status_path"`
	PollCapability       string   `json:"poll_capability"`
	PollArgument         []string `json:"poll_argument"`
	PendingStates        []string `json:"pending_states"`
	SuccessStates        []string `json:"success_states"`
	FailureStates        []string `json:"failure_states"`
	IntervalSeconds      float64  `json:"interval_seconds"`
	TimeoutSeconds       float64  `json:"timeout_seconds"`
	ReconciliationStates []string `json:"reconciliation_states,omitempty"`
	ReconcileOnTimeout   bool     `json:"reconcile_on_timeout,omitempty"`
}

// MarshalJSON preserves canonical floating point units for operation timing.
func (b OperationBinding) MarshalJSON() ([]byte, error) {
	type binding OperationBinding
	raw, err := json.Marshal(binding(b))
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return nil, err
	}
	interval, err := jsonvalue.Float(b.IntervalSeconds)
	if err != nil {
		return nil, err
	}
	timeout, err := jsonvalue.Float(b.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	fields["interval_seconds"] = json.Number(interval)
	fields["timeout_seconds"] = json.Number(timeout)
	return json.Marshal(fields)
}

// UnmarshalJSON rejects unknown fields and invalid timing or overlapping states.
func (b *OperationBinding) UnmarshalJSON(raw []byte) error {
	type binding OperationBinding
	var parsed binding
	if err := jsonvalue.DecodeStrict(raw, &parsed); err != nil {
		return err
	}
	if len(parsed.IDPath) == 0 || len(parsed.StatusPath) == 0 || parsed.PollCapability == "" || len(parsed.PollArgument) == 0 {
		return errors.New("invalid operation binding")
	}
	if parsed.PendingStates == nil {
		parsed.PendingStates = []string{"queued", "running"}
	}
	if parsed.SuccessStates == nil {
		parsed.SuccessStates = []string{"succeeded"}
	}
	if parsed.FailureStates == nil {
		parsed.FailureStates = []string{"failed", "cancelled"}
	}
	if parsed.IntervalSeconds == 0 {
		parsed.IntervalSeconds = 5
	}
	if parsed.TimeoutSeconds == 0 {
		parsed.TimeoutSeconds = 3600
	}
	if parsed.IntervalSeconds < 1 || parsed.IntervalSeconds > 3600 || parsed.TimeoutSeconds <= 0 || parsed.TimeoutSeconds > 604800 {
		return errors.New("invalid operation timing")
	}
	seen := map[string]bool{}
	for _, group := range [][]string{parsed.PendingStates, parsed.SuccessStates, parsed.FailureStates} {
		if len(group) == 0 {
			return errors.New("operation states must be nonempty and disjoint")
		}
		for _, state := range group {
			if seen[state] {
				return errors.New("operation states must be nonempty and disjoint")
			}
			seen[state] = true
		}
	}
	for _, state := range parsed.ReconciliationStates {
		if state == "" || seen[state] {
			return errors.New("operation states must be nonempty and disjoint")
		}
		seen[state] = true
	}
	*b = OperationBinding(parsed)
	return nil
}

// CapabilityDescription declares a capability's contract and execution guarantees.
// Catalog values are immutable after a provider is opened.
type CapabilityDescription struct {
	Name                string            `json:"name"`
	Version             string            `json:"version"`
	Description         string            `json:"description"`
	InputSchema         JSON              `json:"input_schema"`
	Effect              string            `json:"effect"`
	OutputFields        JSON              `json:"output_fields"`
	OutputSchema        JSON              `json:"output_schema"`
	ModelOutput         *ModelOutput      `json:"model_output,omitempty"`
	SkillsList          []string          `json:"skills"`
	Replay              string            `json:"replay"`
	IdempotencyArgument []string          `json:"idempotency_argument"`
	ReferenceScope      string            `json:"reference_scope"`
	ApprovalRequired    bool              `json:"approval_required"`
	Operation           *OperationBinding `json:"operation"`
	SourcePackID        string            `json:"source_pack_id,omitempty"`
}

// MarshalJSON includes default effect, replay and reference scope values.
func (c CapabilityDescription) MarshalJSON() ([]byte, error) {
	type alias CapabilityDescription
	if c.SkillsList == nil {
		c.SkillsList = []string{}
	}
	if c.Effect == "" {
		c.Effect = "read"
	}
	if c.Replay == "" {
		c.Replay = "never"
	}
	if c.ReferenceScope == "" {
		c.ReferenceScope = "durable"
	}
	return json.Marshal(alias(c))
}

// ModelView returns a model catalog entry with bounded input schema detail.
func (c CapabilityDescription) ModelView() JSON {
	return c.modelView(false)
}

// A runtime with a selected catalog can disclose complete contracts first;
// the context budget still defers schemas when the whole packet is too large.
func (c CapabilityDescription) modelView(fullInputSchema bool) JSON {
	b, err := json.Marshal(c)
	if err != nil {
		return JSON{}
	}
	var m JSON
	if err := json.Unmarshal(b, &m); err != nil {
		return JSON{}
	}
	delete(m, "output_schema")
	if strings.HasPrefix(c.Name, "ui.") && c.Operation != nil && c.Operation.PollCapability == "ui.command_status" {
		// The host, rather than the model, applies this browser poll binding.
		// Keep the prerequisite and result source visible without repeating all
		// timeouts, paths and state lists for every page action in every round.
		m["operation"] = JSON{"poll_capability": "ui.command_status"}
		m["requires_browser_context"] = true
	}
	for k, v := range m {
		if v == nil {
			delete(m, k)
		}
	}
	raw, err := json.Marshal(c.InputSchema)
	if err != nil {
		return JSON{}
	}
	if !fullInputSchema && utf8.RuneCount(raw) > 2000 {
		// Union schemas declare the same top-level envelope in their branches.
		// Keep those fields visible even when the full contract is deferred.
		seen := map[string]bool{}
		var collect func(map[string]any)
		collect = func(schema map[string]any) {
			if properties, ok := schema["properties"].(map[string]any); ok {
				for key := range properties {
					seen[key] = true
				}
			}
			for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
				branches, _ := schema[keyword].([]any)
				for _, branch := range branches {
					if child, ok := branch.(map[string]any); ok {
						collect(child)
					}
				}
			}
		}
		if schema, ok := m["input_schema"].(map[string]any); ok {
			collect(schema)
		}
		delete(m, "input_schema")
		fields := []string{}
		for key := range seen {
			fields = append(fields, key)
		}
		sort.Strings(fields)
		if len(fields) > 32 {
			fields = fields[:32]
		}
		m["input_fields"] = fields
		m["schema_requires_inspection"] = true
	}
	return m
}

// SkillDescription identifies a pinned usage guide without its full text.
type SkillDescription struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Skill contains usage guidance; its content cannot expand the caller's grants.
type Skill struct {
	Description SkillDescription
	Content     string
}

// CapabilityResult contains provider data or a safe error code, never both.
// An unknown external outcome must remain distinguishable from a definite failure.
type CapabilityResult struct {
	Data           JSON       `json:"data,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ReferenceScope string     `json:"reference_scope,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// CapabilityProvider owns an opened capability connection.
// Capabilities and Skills return read-only catalogs; Invoke must respect cancellation.
// The opener closes the provider after all invocation goroutines have exited.
type CapabilityProvider interface {
	Capabilities() map[string]CapabilityDescription
	Skills() map[string]Skill
	SystemPrompt() string
	Invoke(context.Context, string, map[string]any, *InvocationContext) (CapabilityResult, error)
	Close() error
}

// invocationValidator checks built-in provider prerequisites without executing
// the capability. Execution still rechecks live state after approval.
type invocationValidator interface {
	validateInvocation(context.Context, string, InvocationContext) error
}

// ConcurrentCapabilityProvider explicitly opts into simultaneous Invoke calls.
// Catalogs and request validation must be safe for concurrent reads.
type ConcurrentCapabilityProvider interface {
	ConcurrentInvocation(capability string) bool
}

// DecisionModel chooses one validated action from a bounded context.
// Implementations must respect cancellation and return errors without exposing secrets.
// Errors with Code() string deliberately expose that safe code; unknown uncoded errors become model_unavailable.
type DecisionModel interface {
	Decide(context.Context, ContextPacket, string) (Decision, error)
}

// CallOutcome carries retained evidence or a structured execution error code.
type CallOutcome struct {
	Fact      *Fact
	ErrorCode string
}

// BindIdempotency copies arguments before binding the existing invocation key.
// It rejects malformed paths or arguments without changing the caller's map.
func BindIdempotency(call ToolCall, cap CapabilityDescription, inv InvocationContext) (ToolCall, error) {
	if cap.IdempotencyArgument == nil {
		return call, nil
	}
	if len(cap.IdempotencyArgument) == 0 {
		return call, errors.New("idempotency_argument_invalid")
	}
	b, err := CanonicalJSON(call.Arguments)
	if err != nil {
		return call, errors.New("capability_input_invalid")
	}
	var args JSON
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return call, errors.New("capability_input_invalid")
	}
	cursor := args
	for _, key := range cap.IdempotencyArgument[:len(cap.IdempotencyArgument)-1] {
		next, ok := cursor[key]
		if !ok {
			child := JSON{}
			cursor[key] = child
			cursor = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return call, errors.New("capability_input_invalid")
		}
		cursor = child
	}
	cursor[cap.IdempotencyArgument[len(cap.IdempotencyArgument)-1]] = inv.IdempotencyKey
	call.Arguments = args
	return call, nil
}

// ExecuteCall checks catalog access and invokes a provider with owned arguments.
// It copies returned evidence and converts provider failures to safe outcome codes.
func ExecuteCall(ctx context.Context, provider CapabilityProvider, grants map[string]bool, call ToolCall, inv *InvocationContext) (CallOutcome, error) {
	cap, ok := provider.Capabilities()[call.Capability]
	if !ok {
		return CallOutcome{ErrorCode: "capability_unknown"}, nil
	}
	if !grants[cap.Name] {
		return CallOutcome{ErrorCode: "capability_not_granted"}, nil
	}
	if validateModelOutput(cap.ModelOutput) != nil {
		return CallOutcome{ErrorCode: "model_output_config_invalid"}, nil
	}
	if inv != nil {
		var err error
		call, err = BindIdempotency(call, cap, *inv)
		if err != nil {
			return CallOutcome{ErrorCode: err.Error()}, nil
		}
	}
	// Provider code receives its own arguments, so it cannot alter the saved
	// invocation or the caller's data after the parameter digest was checked.
	arguments, err := jsonvalue.Clone(call.Arguments)
	if err != nil || arguments == nil {
		return CallOutcome{ErrorCode: "capability_input_invalid"}, nil
	}
	var providerInvocation *InvocationContext
	if inv != nil {
		copy := *inv
		providerInvocation = &copy
	}
	projection, err := jsonvalue.Clone(cap.ModelOutput)
	if err != nil {
		return CallOutcome{ErrorCode: "model_output_config_invalid"}, nil
	}
	result, err := provider.Invoke(ctx, call.Capability, arguments, providerInvocation)
	if err != nil {
		return CallOutcome{ErrorCode: "provider_outcome_unknown"}, nil
	}
	if result.ErrorCode != "" || result.Data == nil {
		return CallOutcome{ErrorCode: firstNonempty(result.ErrorCode, "upstream_response_invalid")}, nil
	}
	data, err := jsonvalue.Clone(result.Data)
	if err != nil {
		return CallOutcome{ErrorCode: "upstream_response_invalid"}, nil
	}
	scope := "durable"
	if result.ReferenceScope == "connection" || cap.ReferenceScope == "connection" {
		scope = "connection"
	}
	fact := Fact{FactID: NewID(), SourceCapability: cap.Name, SourceVersion: cap.Version, Value: JSON{"data": data}, ModelOutput: projection, Quality: "provider_reported", ObservedAt: time.Now().UTC(), ReferenceScope: scope}
	if result.ExpiresAt != nil {
		expiresAt := *result.ExpiresAt
		fact.ExpiresAt = &expiresAt
	}
	if inv != nil {
		connectionID := inv.ConnectionID
		fact.ConnectionID = &connectionID
		fact.SourcePackID, fact.SourceRelease, fact.SourceSubject = inv.TargetPackID, inv.TargetRelease, inv.TargetSubject
	}
	return CallOutcome{Fact: &fact}, nil
}
func firstNonempty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Observe appends evidence and observations and settles the supplied invocation.
func Observe(state *RuntimeState, item *Invocation, outcome CallOutcome) {
	obs := Observation{CallRef: item.Call.CallRef, Capability: item.Call.Capability, Arguments: item.Call.Arguments}
	modelObs := obs
	modelObs.Arguments = item.OriginalArguments
	if outcome.Fact != nil {
		state.Facts = append(state.Facts, *outcome.Fact)
		item.FactID = &outcome.Fact.FactID
		obs.FactID = item.FactID
		modelObs.FactID = item.FactID
		obs.Status = "succeeded"
		modelObs.Status = "succeeded"
		item.Status = "succeeded"
		item.ErrorCode = nil
	} else {
		obs.Status = "failed"
		modelObs.Status = "failed"
		item.Status = "failed"
		item.ErrorCode = strptr(outcome.ErrorCode)
		obs.ErrorCode = item.ErrorCode
		modelObs.ErrorCode = item.ErrorCode
	}
	state.Observations = append(state.Observations, obs)
	state.ModelObservations = append(state.ModelObservations, modelObs)
}

// ArgumentsDigest computes the canonical SHA-256 of a JSON capability call.
// It returns an empty string when the call cannot be encoded as JSON.
func ArgumentsDigest(call ToolCall) string {
	raw, err := CanonicalJSON(JSON{"capability": call.Capability, "arguments": call.Arguments})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func validateResult(r CapabilityResult) error {
	if (r.Data == nil) == (r.ErrorCode == "") {
		return errors.New("capability result must contain data or an error code")
	}
	if r.ErrorCode != "" && len(r.ErrorCode) > 120 {
		return fmt.Errorf("invalid error code")
	}
	return nil
}
