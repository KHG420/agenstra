package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

// SafeCodePattern validates bounded codes safe to carry across provider boundaries.
var SafeCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,120}$`)

// SlotPattern matches declared REST path placeholders.
var SlotPattern = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_]*)\}`)

// ScalarValue formats a finite scalar for REST path, query or header binding.
func ScalarValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case int, int64, float64, json.Number:
		return fmt.Sprint(x), nil
	default:
		return "", errors.New("parameter must be scalar")
	}
}

// RestEndpoint fixes an HTTP operation's input, output and execution contract.
type RestEndpoint struct {
	Name                string             `json:"name"`
	Description         string             `json:"description"`
	Method              string             `json:"method"`
	Path                string             `json:"path"`
	InputSchema         JSON               `json:"input_schema"`
	OutputSchema        JSON               `json:"output_schema"`
	ModelOutput         *ModelOutput       `json:"model_output,omitempty"`
	Effect              string             `json:"effect"`
	ResponsePath        []any              `json:"response_path"`
	ResponseMode        string             `json:"response_mode,omitempty"`
	AllowEmptySuccess   bool               `json:"allow_empty_success,omitempty"`
	BusinessSuccess     *RestBusinessCheck `json:"business_success,omitempty"`
	ResponseSchemas     map[string]JSON    `json:"response_schemas"`
	ErrorCodes          map[string]string  `json:"error_codes"`
	TimeoutSeconds      float64            `json:"timeout_seconds"`
	IdempotencyHeader   *string            `json:"idempotency_header"`
	IdempotencyArgument []string           `json:"idempotency_argument"`
	Operation           *OperationBinding  `json:"operation"`
	Skills              []string           `json:"skills"`
	ApprovalRequired    bool               `json:"approval_required"`
}

// RestBusinessCheck treats a successful HTTP status as a business failure when
// the response field differs from Value. ErrorCode is deliberately namespaced
// so a business response cannot impersonate an authorization or unknown outcome.
type RestBusinessCheck struct {
	Path      []any  `json:"path"`
	Value     any    `json:"value"`
	ErrorCode string `json:"error_code"`
}

// UnmarshalJSON strictly decodes an endpoint and applies the default timeout.
func (e *RestEndpoint) UnmarshalJSON(raw []byte) error {
	type endpoint RestEndpoint
	var parsed endpoint
	if err := jsonvalue.DecodeStrict(raw, &parsed); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return err
	}
	if _, ok := keys["timeout_seconds"]; !ok {
		parsed.TimeoutSeconds = 20
	}
	*e = RestEndpoint(parsed)
	return nil
}

// RestManifest declares trusted REST endpoints and credential references.
type RestManifest struct {
	Schema       string            `json:"schema"`
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Guidance     string            `json:"guidance"`
	BaseURLEnv   string            `json:"base_url_env"`
	TokenEnv     *string           `json:"token_env"`
	HeadersEnv   map[string]string `json:"headers_env"`
	Capabilities []RestEndpoint    `json:"capabilities"`
	Skills       []SkillFile       `json:"skills"`
}

// RestField describes a primitive field in the legacy REST contract.
type RestField struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// UnmarshalJSON strictly decodes a field and makes it required by default.
func (f *RestField) UnmarshalJSON(raw []byte) error {
	var holder struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Required    *bool  `json:"required"`
	}
	if err := jsonvalue.DecodeStrict(raw, &holder); err != nil {
		return err
	}
	f.Type = holder.Type
	f.Description = holder.Description
	f.Required = true
	if holder.Required != nil {
		f.Required = *holder.Required
	}
	return nil
}

// LegacyRestCapability declares a fixed endpoint and flat input/output fields.
type LegacyRestCapability struct {
	Name           string               `json:"name"`
	Version        string               `json:"version"`
	Description    string               `json:"description"`
	Method         string               `json:"method"`
	URLEnv         string               `json:"url_env"`
	TokenEnv       *string              `json:"token_env"`
	ResponsePath   []any                `json:"response_path"`
	Inputs         map[string]RestField `json:"inputs"`
	Outputs        map[string]RestField `json:"outputs"`
	TimeoutSeconds float64              `json:"timeout_seconds"`
	MaxAttempts    int                  `json:"max_attempts"`
}

// UnmarshalJSON strictly decodes a legacy endpoint and applies its defaults.
func (c *LegacyRestCapability) UnmarshalJSON(raw []byte) error {
	type capability LegacyRestCapability
	var parsed capability
	if err := jsonvalue.DecodeStrict(raw, &parsed); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return err
	}
	if _, ok := keys["method"]; !ok {
		parsed.Method = "POST"
	}
	if _, ok := keys["timeout_seconds"]; !ok {
		parsed.TimeoutSeconds = 20
	}
	if _, ok := keys["max_attempts"]; !ok {
		parsed.MaxAttempts = 1
	}
	*c = LegacyRestCapability(parsed)
	return nil
}

// LegacyPackManifest is the original flat REST capability pack format.
type LegacyPackManifest struct {
	Schema       string                 `json:"schema"`
	Name         string                 `json:"name"`
	Guidance     string                 `json:"guidance"`
	Capabilities []LegacyRestCapability `json:"capabilities"`
}
