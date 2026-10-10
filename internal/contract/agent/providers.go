package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
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
	return c.CatalogView(false)
}

// CatalogView builds the model catalog entry with optional complete input-schema detail.
// A runtime with a selected catalog can disclose complete contracts first;
// the context budget still defers schemas when the whole packet is too large.
func (c CapabilityDescription) CatalogView(fullInputSchema bool) JSON {
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
// Error codes use 1–120 ASCII letters, digits, underscores, dots, colons or hyphens.
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

// InvocationValidator checks built-in provider prerequisites without executing
// the capability. Execution still rechecks live state after approval.
type InvocationValidator interface {
	ValidateInvocation(context.Context, string, InvocationContext) error
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

// SkillFile pins a usage guide's path, metadata and SHA-256 digest.
type SkillFile struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
}

// MCPDiscoveredTool describes an available tool without granting or invoking it.
// Remote annotations are not trusted as authorization or replay guarantees.
type MCPDiscoveredTool struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	InputSchema    JSON            `json:"input_schema"`
	OutputSchema   JSON            `json:"output_schema"`
	ContractSHA256 string          `json:"contract_sha256"`
	Supported      bool            `json:"supported"`
	Issue          string          `json:"issue,omitempty"`
	Exposure       MCPToolExposure `json:"exposure"`
}

// ModelOutput limits which fields of a provider result may enter model context
// or be read through a Fact reference. Paths are relative to the result data.
// A nil ModelOutput keeps the existing full-result behavior; an empty Paths
// list makes the result opaque to the model.
type ModelOutput struct {
	Paths [][]string `json:"paths"`
}

// ValidateModelOutput checks the configured object-field projection without accessing evidence.
func ValidateModelOutput(output *ModelOutput) error {
	if output == nil {
		return nil
	}
	if output.Paths == nil || len(output.Paths) > 64 {
		return fmt.Errorf("model_output paths must be an array of at most 64 paths")
	}
	seen := map[string]bool{}
	for _, path := range output.Paths {
		if len(path) == 0 || len(path) > 16 {
			return fmt.Errorf("model_output path must contain 1..16 object fields")
		}
		key := ""
		for _, field := range path {
			if field == "" || len(field) > 128 {
				return fmt.Errorf("model_output path contains an invalid field")
			}
			key += fmt.Sprintf("%d:%s", len(field), field)
		}
		if seen[key] {
			return fmt.Errorf("model_output paths must be distinct")
		}
		seen[key] = true
	}
	return nil
}

// ValidateModelOutputSchema ensures every projected path exists in the declared object schema.
func ValidateModelOutputSchema(output *ModelOutput, schema JSON) error {
	if output == nil {
		return nil
	}
	for _, path := range output.Paths {
		var current JSON = schema
		for _, field := range path {
			for depth := 0; depth < 16; depth++ {
				ref, ok := current["$ref"].(string)
				if !ok {
					break
				}
				if !strings.HasPrefix(ref, "#/$defs/") {
					return fmt.Errorf("model_output path requires a local object schema")
				}
				defs, _ := schema["$defs"].(map[string]any)
				current, _ = defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
				if current == nil {
					return fmt.Errorf("model_output path has an unresolved schema reference")
				}
			}
			if current["type"] != "object" {
				return fmt.Errorf("model_output path may only traverse object fields")
			}
			properties, _ := current["properties"].(map[string]any)
			current, _ = properties[field].(map[string]any)
			if current == nil {
				return fmt.Errorf("model_output path is not declared in output_schema")
			}
		}
	}
	return nil
}

func projectedModelData(data JSON, output *ModelOutput) JSON {
	if output == nil {
		return data
	}
	visible := JSON{}
	if ValidateModelOutput(output) != nil {
		return visible
	}
	for _, path := range output.Paths {
		var value any = data
		for _, field := range path {
			object, ok := value.(map[string]any)
			if !ok {
				value = nil
				break
			}
			var exists bool
			value, exists = object[field]
			if !exists {
				value = nil
				break
			}
		}
		if value == nil {

			// A declared null is visible, while a missing path is not.
			if !modelPathExists(data, path) {
				continue
			}
		}
		cursor := visible
		for _, field := range path[:len(path)-1] {
			next, ok := cursor[field].(map[string]any)
			if !ok {
				next = JSON{}
				cursor[field] = next
			}
			cursor = next
		}
		cursor[path[len(path)-1]] = value
	}
	return visible
}

func modelPathExists(data JSON, path []string) bool {
	var value any = data
	for _, field := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		var exists bool
		value, exists = object[field]
		if !exists {
			return false
		}
	}
	return true
}

// ModelFactValue projects the model-visible evidence while retaining the original fact value.
func ModelFactValue(fact Fact) JSON {
	if fact.ModelOutput == nil {
		return fact.Value
	}
	data, _ := fact.Value["data"].(map[string]any)
	return JSON{"data": projectedModelData(data, fact.ModelOutput)}
}
