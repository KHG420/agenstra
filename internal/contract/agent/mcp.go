package agent

import (
	"encoding/json"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

// MCPSource selects a trusted stdio or HTTP transport and connection references.
type MCPSource struct {
	Transport      string            `json:"transport"`
	Command        *string           `json:"command"`
	Args           []string          `json:"args"`
	CWDEnv         *string           `json:"cwd_env"`
	Environment    map[string]string `json:"environment"`
	URLEnv         *string           `json:"url_env"`
	TokenEnv       *string           `json:"token_env"`
	TimeoutSeconds float64           `json:"timeout_seconds"`
}

// UnmarshalJSON strictly decodes transport configuration and applies timeout defaults.
func (s *MCPSource) UnmarshalJSON(raw []byte) error {
	type source MCPSource
	var parsed source
	if err := jsonvalue.DecodeStrict(raw, &parsed); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if _, ok := fields["timeout_seconds"]; !ok {
		parsed.TimeoutSeconds = 60
	}
	*s = MCPSource(parsed)
	return nil
}

// MCPToolExposure pins an exposed tool's full contract and execution guarantees.
type MCPToolExposure struct {
	Name                string            `json:"name"`
	Effect              string            `json:"effect"`
	Optional            bool              `json:"optional"`
	Skills              []string          `json:"skills"`
	ContractSHA256      string            `json:"contract_sha256"`
	Replay              string            `json:"replay"`
	IdempotencyArgument []string          `json:"idempotency_argument"`
	ReferenceScope      string            `json:"reference_scope"`
	ExpiresAtPath       []string          `json:"expires_at_path"`
	ApprovalRequired    bool              `json:"approval_required"`
	Operation           *OperationBinding `json:"operation"`
}

// UnmarshalJSON strictly decodes a tool exposure and applies execution defaults.
func (e *MCPToolExposure) UnmarshalJSON(raw []byte) error {
	type exposure MCPToolExposure
	var parsed exposure
	if err := jsonvalue.DecodeStrict(raw, &parsed); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if _, ok := fields["replay"]; !ok {
		parsed.Replay = "never"
	}
	if _, ok := fields["reference_scope"]; !ok {
		parsed.ReferenceScope = "durable"
	}
	*e = MCPToolExposure(parsed)
	return nil
}

// MCPManifest declares pinned MCP tools, connection references and skill files.
type MCPManifest struct {
	Schema        string            `json:"schema"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	Guidance      string            `json:"guidance"`
	Source        MCPSource         `json:"source"`
	Tools         []MCPToolExposure `json:"tools"`
	Skills        []SkillFile       `json:"skills"`
	ErrorCodePath []string          `json:"error_code_path"`
}
