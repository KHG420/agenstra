package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	"github.com/KHG420/agenstra/internal/platform/mcptransport"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
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
type mcpTransport interface {
	Request(context.Context, string, JSON) (JSON, error)
	Notify(context.Context, string, JSON) error
	Close() error
}

// MCPPack owns an MCP transport and validated tool catalogs.
// Close it only after requests have stopped; catalogs remain read-only.
type MCPPack struct {
	Manifest     MCPManifest
	transport    mcpTransport
	capabilities map[string]CapabilityDescription
	skills       map[string]Skill
	exposures    map[string]MCPToolExposure
	inputs       map[string]*jsonschema.Schema
	outputs      map[string]*jsonschema.Schema
}

// Capabilities returns the read-only capability catalog; callers must not mutate it.
func (p *MCPPack) Capabilities() map[string]CapabilityDescription { return p.capabilities }

// Skills returns read-only pinned usage guides; callers must not mutate the map.
func (p *MCPPack) Skills() map[string]Skill { return p.skills }

// SystemPrompt returns fixed usage guidance without connection credentials.
func (p *MCPPack) SystemPrompt() string { return AgentPrompt(p.Manifest.Guidance) }

// Close releases owned connection resources after outstanding calls have stopped.
func (p *MCPPack) Close() error { return p.transport.Close() }

// MCPContractDigest computes the canonical digest of a JSON tool contract.
// It returns an empty string for an invalid JSON contract.
func MCPContractDigest(tool JSON) string {
	raw, err := CanonicalJSON(tool)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// OpenMCPPack opens and initializes a transport, rejecting any pinned tool contract drift.
// The caller must close the returned pack.
func OpenMCPPack(ctx context.Context, path string, environment map[string]string) (*MCPPack, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest MCPManifest
	if err := jsonvalue.DecodeStrict(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != "agenstra.mcp-pack.v1" || manifest.Name == "" || manifest.Version == "" || manifest.Guidance == "" || len(manifest.Tools) == 0 {
		return nil, errors.New("invalid MCP pack manifest")
	}
	env := environmentOrOS(environment)
	skills, err := LoadSkillFiles(manifest.Skills, filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	skillContents := map[string]string{}
	for _, entry := range manifest.Skills {
		skillContents[entry.Path] = skills[entry.Name].Content
	}
	if err := validateRawManifest(raw, skillContents); err != nil {
		return nil, err
	}
	transport, remote, err := openMCPSource(ctx, manifest.Source, env)
	if err != nil {
		return nil, err
	}
	pack := &MCPPack{Manifest: manifest, transport: transport, capabilities: map[string]CapabilityDescription{}, skills: skills, exposures: map[string]MCPToolExposure{}, inputs: map[string]*jsonschema.Schema{}, outputs: map[string]*jsonschema.Schema{}}
	seen := map[string]bool{}
	for _, ex := range manifest.Tools {
		if seen[ex.Name] {
			return nil, errors.Join(errors.New("duplicate capability: "+ex.Name), transport.Close())
		}
		seen[ex.Name] = true
		for _, skill := range ex.Skills {
			if _, ok := skills[skill]; !ok {
				return nil, errors.Join(errors.New("unknown skill for capability: "+ex.Name), transport.Close())
			}
		}
		tool, ok := remote[ex.Name]
		if !ok {
			if ex.Optional {
				continue
			}
			return nil, errors.Join(errors.New("required capability unavailable: "+ex.Name), transport.Close())
		}
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(ex.ContractSHA256) || MCPContractDigest(tool) != ex.ContractSHA256 {
			return nil, errors.Join(errors.New("capability contract changed: "+ex.Name), transport.Close())
		}
		input, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			return nil, errors.Join(errors.New("input schema required"), transport.Close())
		}
		output, ok := tool["outputSchema"].(map[string]any)
		if !ok {
			return nil, errors.Join(errors.New("structured output schema required: "+ex.Name), transport.Close())
		}
		iv, e := validateLocalSchema(input, false)
		if e != nil {
			return nil, errors.Join(e, transport.Close())
		}
		ov, e := validateLocalSchema(output, false)
		if e != nil {
			return nil, errors.Join(e, transport.Close())
		}
		description, _ := tool["description"].(string)
		if description == "" {
			description = ex.Name
		}
		if ex.ReferenceScope == "" {
			ex.ReferenceScope = "durable"
		}
		if ex.Replay == "" {
			ex.Replay = "never"
		}
		pack.exposures[ex.Name] = ex
		pack.inputs[ex.Name] = iv
		pack.outputs[ex.Name] = ov
		pack.capabilities[ex.Name] = CapabilityDescription{Name: ex.Name, Version: manifest.Version, Description: description, InputSchema: input, OutputSchema: output, Effect: ex.Effect, SkillsList: ex.Skills, Replay: ex.Replay, IdempotencyArgument: ex.IdempotencyArgument, ReferenceScope: ex.ReferenceScope, ApprovalRequired: ex.ApprovalRequired, Operation: ex.Operation}
	}
	return pack, nil
}

// Invoke validates and executes the selected capability with request cancellation.
// Provider failures use the structured ErrorCode channel when their outcome is known.
func (p *MCPPack) Invoke(ctx context.Context, name string, args map[string]any, _ *InvocationContext) (CapabilityResult, error) {
	if p.capabilities[name].Name == "" {
		return CapabilityResult{ErrorCode: "capability_unknown"}, nil
	}
	if validateSchema(p.inputs[name], args) != nil {
		return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
	}
	timeout := p.Manifest.Source.TimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
	defer cancel()
	reply, err := p.transport.Request(callCtx, "tools/call", JSON{"name": name, "arguments": args})
	if err != nil {
		return CapabilityResult{ErrorCode: "provider_outcome_unknown"}, nil
	}
	if reply["isError"] == true {
		var selected any = reply["structuredContent"]
		for _, key := range p.Manifest.ErrorCodePath {
			m, ok := selected.(map[string]any)
			if !ok {
				selected = nil
				break
			}
			selected = m[key]
		}
		code, ok := selected.(string)
		if !ok || !safeCodePattern.MatchString(code) {
			code = "capability_failed"
		}
		return CapabilityResult{ErrorCode: code}, nil
	}
	data, ok := reply["structuredContent"].(map[string]any)
	if !ok || validateSchema(p.outputs[name], data) != nil {
		return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	ex := p.exposures[name]
	var expiry *time.Time
	if len(ex.ExpiresAtPath) > 0 {
		var selected any = data
		for _, key := range ex.ExpiresAtPath {
			m, ok := selected.(map[string]any)
			if !ok {
				return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
			}
			selected = m[key]
		}
		s, ok := selected.(string)
		if !ok {
			return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		parsed, e := time.Parse(time.RFC3339Nano, s)
		if e != nil {
			return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		expiry = &parsed
	}
	return CapabilityResult{Data: data, ReferenceScope: ex.ReferenceScope, ExpiresAt: expiry}, nil
}

// openMCPSource is shared by discovery and execution so discovery pins the same
// complete contract that execution will subsequently verify.
func openMCPSource(ctx context.Context, source MCPSource, env map[string]string) (mcpTransport, map[string]JSON, error) {
	if source.TimeoutSeconds == 0 {
		source.TimeoutSeconds = 60
	}
	if source.TimeoutSeconds <= 0 || source.TimeoutSeconds > 300 {
		return nil, nil, errors.New("invalid MCP timeout")
	}
	var transport mcpTransport
	if source.Transport == "stdio" {
		if source.Command == nil || *source.Command == "" || source.URLEnv != nil || source.TokenEnv != nil {
			return nil, nil, errors.New("stdio requires a command and environment-based credentials")
		}
		cmd := exec.CommandContext(ctx, *source.Command, source.Args...)
		if source.CWDEnv != nil {
			cwd, e := requiredEnv(env, *source.CWDEnv)
			if e != nil {
				return nil, nil, e
			}
			cmd.Dir = cwd
		}
		cmd.Env = os.Environ()
		for target, origin := range source.Environment {
			v, e := requiredEnv(env, origin)
			if e != nil {
				return nil, nil, e
			}
			cmd.Env = append(cmd.Env, target+"="+v)
		}
		client, e := mcptransport.StartStdio(cmd)
		if e != nil {
			return nil, nil, e
		}
		transport = client
	} else if source.Transport == "streamable_http" {
		if source.URLEnv == nil || source.Command != nil || len(source.Args) > 0 || source.CWDEnv != nil || len(source.Environment) > 0 {
			return nil, nil, errors.New("streamable_http requires url_env and optional token_env")
		}
		target, e := requiredEnv(env, *source.URLEnv)
		if e != nil {
			return nil, nil, e
		}
		u, e := url.Parse(target)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, nil, errors.New("invalid MCP URL")
		}
		token := ""
		if source.TokenEnv != nil {
			token, e = requiredEnv(env, *source.TokenEnv)
			if e != nil {
				return nil, nil, e
			}
		}
		transport = mcptransport.NewHTTP(target, token, &http.Client{Timeout: time.Duration(source.TimeoutSeconds * float64(time.Second)), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	} else {
		return nil, nil, errors.New("invalid MCP transport")
	}
	initParams := JSON{"protocolVersion": "2025-03-26", "capabilities": JSON{}, "clientInfo": JSON{"name": "agenstra", "version": "1.0.0"}}
	// Bound startup exchanges as well as business calls. Keep the subprocess
	// attached to the caller's context, not these short-lived request contexts.
	requestTimeout := time.Duration(source.TimeoutSeconds * float64(time.Second))
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	initialized, err := transport.Request(requestCtx, "initialize", initParams)
	cancel()
	if err != nil {
		return nil, nil, errors.Join(err, transport.Close())
	}
	if client, ok := transport.(*mcptransport.HTTP); ok {
		if version, ok := initialized["protocolVersion"].(string); ok && version != "" {
			client.SetProtocolVersion(version)
		}
	}
	requestCtx, cancel = context.WithTimeout(ctx, requestTimeout)
	err = transport.Notify(requestCtx, "notifications/initialized", JSON{})
	cancel()
	if err != nil {
		return nil, nil, errors.Join(err, transport.Close())
	}
	remote := map[string]JSON{}
	cursors := map[string]bool{}
	cursor := ""
	for {
		params := JSON{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		page, e := transport.Request(requestCtx, "tools/list", params)
		cancel()
		if e != nil {
			return nil, nil, errors.Join(e, transport.Close())
		}
		items, ok := page["tools"].([]any)
		if !ok {
			return nil, nil, errors.Join(errors.New("invalid MCP tools list"), transport.Close())
		}
		for _, x := range items {
			tool, ok := x.(map[string]any)
			if !ok {
				return nil, nil, errors.Join(errors.New("invalid MCP tool"), transport.Close())
			}
			name, _ := tool["name"].(string)
			if name == "" || remote[name] != nil {
				return nil, nil, errors.Join(errors.New("duplicate remote capability"), transport.Close())
			}
			remote[name] = tool
		}
		next, _ := page["nextCursor"].(string)
		if next == "" {
			break
		}
		if cursors[next] {
			return nil, nil, errors.Join(errors.New("capability pagination repeated cursor"), transport.Close())
		}
		cursors[next] = true
		cursor = next
	}
	return transport, remote, nil
}
