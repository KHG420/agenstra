package capability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/platform/mcptransport"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type mcpTransport interface {
	Request(context.Context, string, agentcontract.JSON) (agentcontract.JSON, error)
	Notify(context.Context, string, agentcontract.JSON) error
	Close() error
}

// MCPPack owns an MCP transport and validated tool catalogs.
// Close it only after requests have stopped; catalogs remain read-only.
type MCPPack struct {
	Manifest     agentcontract.MCPManifest
	transport    mcpTransport
	capabilities map[string]agentcontract.CapabilityDescription
	skills       map[string]agentcontract.Skill
	exposures    map[string]agentcontract.MCPToolExposure
	inputs       map[string]*jsonschema.Schema
	outputs      map[string]*jsonschema.Schema
}

// Capabilities returns the read-only capability catalog; callers must not mutate it.
func (p *MCPPack) Capabilities() map[string]agentcontract.CapabilityDescription {
	return p.capabilities
}

// Skills returns read-only pinned usage guides; callers must not mutate the map.
func (p *MCPPack) Skills() map[string]agentcontract.Skill { return p.skills }

// SystemPrompt returns fixed usage guidance without connection credentials.
func (p *MCPPack) SystemPrompt() string { return AgentPrompt(p.Manifest.Guidance) }

// Close releases owned connection resources after outstanding calls have stopped.
func (p *MCPPack) Close() error { return p.transport.Close() }

// MCPContractDigest computes the canonical digest of a JSON tool contract.
// It returns an empty string for an invalid JSON contract.
func MCPContractDigest(tool agentcontract.JSON) string {
	raw, err := agentcontract.CanonicalJSON(tool)
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
	var manifest agentcontract.MCPManifest
	if err := jsonvalue.DecodeStrict(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != "agenstra.mcp-pack.v1" || manifest.Name == "" || manifest.Version == "" || manifest.Guidance == "" || len(manifest.Tools) == 0 {
		return nil, errors.New("invalid MCP pack manifest")
	}
	env := EnvironmentOrOS(environment)
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
	transport, remote, err := OpenMCPSource(ctx, manifest.Source, env)
	if err != nil {
		return nil, err
	}
	pack := &MCPPack{Manifest: manifest, transport: transport, capabilities: map[string]agentcontract.CapabilityDescription{}, skills: skills, exposures: map[string]agentcontract.MCPToolExposure{}, inputs: map[string]*jsonschema.Schema{}, outputs: map[string]*jsonschema.Schema{}}
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
		iv, e := agentcontract.ValidateLocalSchema(input, false)
		if e != nil {
			return nil, errors.Join(e, transport.Close())
		}
		ov, e := agentcontract.ValidateLocalSchema(output, false)
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
		pack.capabilities[ex.Name] = agentcontract.CapabilityDescription{Name: ex.Name, Version: manifest.Version, Description: description, InputSchema: input, OutputSchema: output, Effect: ex.Effect, SkillsList: ex.Skills, Replay: ex.Replay, IdempotencyArgument: ex.IdempotencyArgument, ReferenceScope: ex.ReferenceScope, ApprovalRequired: ex.ApprovalRequired, Operation: ex.Operation}
	}
	return pack, nil
}

// Invoke validates and executes the selected capability with request cancellation.
// Provider failures use the structured ErrorCode channel when their outcome is known.
func (p *MCPPack) Invoke(ctx context.Context, name string, args map[string]any, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
	if p.capabilities[name].Name == "" {
		return agentcontract.CapabilityResult{ErrorCode: "capability_unknown"}, nil
	}
	if agentcontract.ValidateSchema(p.inputs[name], args) != nil {
		return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
	}
	timeout := p.Manifest.Source.TimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
	defer cancel()
	reply, err := p.transport.Request(callCtx, "tools/call", agentcontract.JSON{"name": name, "arguments": args})
	if err != nil {
		return agentcontract.CapabilityResult{ErrorCode: "provider_outcome_unknown"}, nil
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
		if !ok || !agentcontract.SafeCodePattern.MatchString(code) {
			code = "capability_failed"
		}
		return agentcontract.CapabilityResult{ErrorCode: code}, nil
	}
	data, ok := reply["structuredContent"].(map[string]any)
	if !ok || agentcontract.ValidateSchema(p.outputs[name], data) != nil {
		return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	ex := p.exposures[name]
	var expiry *time.Time
	if len(ex.ExpiresAtPath) > 0 {
		var selected any = data
		for _, key := range ex.ExpiresAtPath {
			m, ok := selected.(map[string]any)
			if !ok {
				return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
			}
			selected = m[key]
		}
		s, ok := selected.(string)
		if !ok {
			return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		parsed, e := time.Parse(time.RFC3339Nano, s)
		if e != nil {
			return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		expiry = &parsed
	}
	return agentcontract.CapabilityResult{Data: data, ReferenceScope: ex.ReferenceScope, ExpiresAt: expiry}, nil
}

// OpenMCPSource opens only the trusted configured transport and leaves cleanup to its caller.
// openMCPSource is shared by discovery and execution so discovery pins the same
// complete contract that execution will subsequently verify.
func OpenMCPSource(ctx context.Context, source agentcontract.MCPSource, env map[string]string) (mcpTransport, map[string]agentcontract.JSON, error) {
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
	initParams := agentcontract.JSON{"protocolVersion": "2025-03-26", "capabilities": agentcontract.JSON{}, "clientInfo": agentcontract.JSON{"name": "agenstra", "version": "1.0.0"}}

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
	err = transport.Notify(requestCtx, "notifications/initialized", agentcontract.JSON{})
	cancel()
	if err != nil {
		return nil, nil, errors.Join(err, transport.Close())
	}
	remote := map[string]agentcontract.JSON{}
	cursors := map[string]bool{}
	cursor := ""
	for {
		params := agentcontract.JSON{}
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
