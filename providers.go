package agenstra

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

type InvocationContext struct {
	RunID          string `json:"run_id"`
	InvocationID   string `json:"invocation_id"`
	IdempotencyKey string `json:"idempotency_key"`
	OwnerID        string `json:"owner_id"`
	ConnectionID   string `json:"connection_id"`
}
type OperationBinding struct {
	IDPath          []any    `json:"id_path"`
	StatusPath      []any    `json:"status_path"`
	PollCapability  string   `json:"poll_capability"`
	PollArgument    []string `json:"poll_argument"`
	PendingStates   []string `json:"pending_states"`
	SuccessStates   []string `json:"success_states"`
	FailureStates   []string `json:"failure_states"`
	IntervalSeconds float64  `json:"interval_seconds"`
	TimeoutSeconds  float64  `json:"timeout_seconds"`
}

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
	interval, err := pythonFloat(b.IntervalSeconds)
	if err != nil {
		return nil, err
	}
	timeout, err := pythonFloat(b.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	fields["interval_seconds"] = json.Number(interval)
	fields["timeout_seconds"] = json.Number(timeout)
	return json.Marshal(fields)
}

func (b *OperationBinding) UnmarshalJSON(raw []byte) error {
	type binding OperationBinding
	var parsed binding
	if err := strictUnmarshal(raw, &parsed); err != nil {
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
	*b = OperationBinding(parsed)
	return nil
}

type CapabilityDescription struct {
	Name                string            `json:"name"`
	Version             string            `json:"version"`
	Description         string            `json:"description"`
	InputSchema         JSON              `json:"input_schema"`
	Effect              string            `json:"effect"`
	OutputFields        JSON              `json:"output_fields"`
	OutputSchema        JSON              `json:"output_schema"`
	SkillsList          []string          `json:"skills"`
	Replay              string            `json:"replay"`
	IdempotencyArgument []string          `json:"idempotency_argument"`
	ReferenceScope      string            `json:"reference_scope"`
	ApprovalRequired    bool              `json:"approval_required"`
	Operation           *OperationBinding `json:"operation"`
}

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
func (c CapabilityDescription) ModelView() JSON {
	b, _ := json.Marshal(c)
	var m JSON
	_ = json.Unmarshal(b, &m)
	delete(m, "output_schema")
	for k, v := range m {
		if v == nil {
			delete(m, k)
		}
	}
	raw, _ := json.Marshal(c.InputSchema)
	if utf8.RuneCount(raw) > 2000 {
		delete(m, "input_schema")
		fields := []string{}
		if p, ok := c.InputSchema["properties"].(map[string]any); ok {
			for k := range p {
				if len(fields) >= 32 {
					break
				}
				fields = append(fields, k)
			}
		}
		m["input_fields"] = fields
		m["schema_requires_inspection"] = true
	}
	return m
}

type SkillDescription struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Skill struct {
	Description SkillDescription
	Content     string
}
type CapabilityResult struct {
	Data           JSON       `json:"data,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ReferenceScope string     `json:"reference_scope,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}
type CapabilityProvider interface {
	Capabilities() map[string]CapabilityDescription
	Skills() map[string]Skill
	SystemPrompt() string
	Invoke(context.Context, string, map[string]any, *InvocationContext) (CapabilityResult, error)
	Close() error
}
type DecisionModel interface {
	Decide(context.Context, ContextPacket, string) (Decision, error)
}

type CallOutcome struct {
	Fact      *Fact
	ErrorCode string
}

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
func ExecuteCall(ctx context.Context, provider CapabilityProvider, grants map[string]bool, call ToolCall, inv *InvocationContext) (CallOutcome, error) {
	cap, ok := provider.Capabilities()[call.Capability]
	if !ok {
		return CallOutcome{ErrorCode: "capability_unknown"}, nil
	}
	if cap.Effect != "read" && !grants[cap.Name] {
		return CallOutcome{ErrorCode: "capability_not_granted"}, nil
	}
	if inv != nil {
		var err error
		call, err = BindIdempotency(call, cap, *inv)
		if err != nil {
			return CallOutcome{ErrorCode: err.Error()}, nil
		}
	}
	result, err := provider.Invoke(ctx, call.Capability, call.Arguments, inv)
	if err != nil {
		return CallOutcome{ErrorCode: "provider_outcome_unknown"}, nil
	}
	if result.ErrorCode != "" || result.Data == nil {
		return CallOutcome{ErrorCode: firstNonempty(result.ErrorCode, "upstream_response_invalid")}, nil
	}
	scope := "durable"
	if result.ReferenceScope == "connection" || cap.ReferenceScope == "connection" {
		scope = "connection"
	}
	fact := Fact{FactID: NewID(), SourceCapability: cap.Name, SourceVersion: cap.Version, Value: JSON{"data": result.Data}, Quality: "provider_reported", ObservedAt: time.Now().UTC(), ReferenceScope: scope, ExpiresAt: result.ExpiresAt}
	if inv != nil {
		fact.ConnectionID = &inv.ConnectionID
	}
	return CallOutcome{Fact: &fact}, nil
}
func firstNonempty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
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
func ArgumentsDigest(call ToolCall) string {
	raw, _ := CanonicalJSON(JSON{"capability": call.Capability, "arguments": call.Arguments})
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
