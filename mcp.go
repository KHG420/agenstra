package agenstra

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

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

func (s *MCPSource) UnmarshalJSON(raw []byte) error {
	type source MCPSource
	var parsed source
	if err := strictUnmarshal(raw, &parsed); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["timeout_seconds"]; !ok {
		parsed.TimeoutSeconds = 60
	}
	*s = MCPSource(parsed)
	return nil
}

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

func (e *MCPToolExposure) UnmarshalJSON(raw []byte) error {
	type exposure MCPToolExposure
	var parsed exposure
	if err := strictUnmarshal(raw, &parsed); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["replay"]; !ok {
		parsed.Replay = "never"
	}
	if _, ok := fields["reference_scope"]; !ok {
		parsed.ReferenceScope = "durable"
	}
	*e = MCPToolExposure(parsed)
	return nil
}

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
type stdioMCP struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	mu        sync.Mutex
	nextID    int
	closeOnce sync.Once
	closeErr  error
}

func (c *stdioMCP) request(ctx context.Context, method string, params JSON, notification bool) (JSON, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	msg := JSON{"jsonrpc": "2.0", "method": method, "params": params}
	if !notification {
		c.nextID++
		msg["id"] = c.nextID
	}
	raw, err := CanonicalJSON(msg)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if _, err = c.stdin.Write(raw); err != nil {
		return nil, err
	}
	if notification {
		return nil, nil
	}
	type readResult struct {
		line []byte
		err  error
	}
	for {
		read := make(chan readResult, 1)
		go func() { line, err := c.stdout.ReadBytes('\n'); read <- readResult{line, err} }()
		var got readResult
		select {
		case <-ctx.Done():
			_ = c.Close()
			return nil, ctx.Err()
		case got = <-read:
		}
		line, err := got.line, got.err
		if err != nil {
			return nil, err
		}
		var reply JSON
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if dec.Decode(&reply) != nil {
			continue
		}
		if reply["id"] == nil {
			continue
		}
		id, ok := pathIndex(reply["id"])
		if !ok || id != c.nextID {
			continue
		}
		if reply["error"] != nil {
			return nil, fmt.Errorf("MCP error: %v", reply["error"])
		}
		result, ok := reply["result"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid MCP result")
		}
		return result, nil
	}
}
func (c *stdioMCP) Request(ctx context.Context, m string, p JSON) (JSON, error) {
	return c.request(ctx, m, p, false)
}
func (c *stdioMCP) Notify(ctx context.Context, m string, p JSON) error {
	_, e := c.request(ctx, m, p, true)
	return e
}
func (c *stdioMCP) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		c.closeErr = c.cmd.Wait()
		if _, ok := c.closeErr.(*exec.ExitError); ok {
			c.closeErr = nil
		}
	})
	return c.closeErr
}

type httpMCP struct {
	url       string
	token     string
	client    *http.Client
	session   string
	protocol  string
	mu        sync.Mutex
	nextID    int
	closeOnce sync.Once
	closeErr  error
}

func (c *httpMCP) request(ctx context.Context, method string, params JSON, notification bool) (JSON, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	msg := JSON{"jsonrpc": "2.0", "method": method, "params": params}
	if !notification {
		c.nextID++
		msg["id"] = c.nextID
	}
	raw, err := CanonicalJSON(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	version := c.protocol
	if version == "" {
		version = "2025-03-26"
	}
	req.Header.Set("Mcp-Protocol-Version", version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP HTTP %d", res.StatusCode)
	}
	if session := res.Header.Get("Mcp-Session-Id"); session != "" {
		c.session = session
	}
	if notification {
		return nil, nil
	}
	var reply JSON
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(io.LimitReader(res.Body, 16<<20))
		scanner.Buffer(make([]byte, 4096), 16<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				dec := json.NewDecoder(strings.NewReader(payload))
				dec.UseNumber()
				if dec.Decode(&reply) == nil && reply["id"] != nil {
					break
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	} else {
		dec := json.NewDecoder(io.LimitReader(res.Body, 16<<20))
		dec.UseNumber()
		if err := dec.Decode(&reply); err != nil {
			return nil, err
		}
	}
	if reply["error"] != nil {
		return nil, fmt.Errorf("MCP error: %v", reply["error"])
	}
	id, ok := pathIndex(reply["id"])
	if !ok || id != c.nextID {
		return nil, errors.New("invalid MCP response id")
	}
	result, ok := reply["result"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid MCP result")
	}
	return result, nil
}
func (c *httpMCP) Request(ctx context.Context, m string, p JSON) (JSON, error) {
	return c.request(ctx, m, p, false)
}
func (c *httpMCP) Notify(ctx context.Context, m string, p JSON) error {
	_, e := c.request(ctx, m, p, true)
	return e
}
func (c *httpMCP) Close() error {
	c.closeOnce.Do(func() {
		if c.session == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
		if err != nil {
			c.closeErr = err
			return
		}
		req.Header.Set("Mcp-Session-Id", c.session)
		version := c.protocol
		if version == "" {
			version = "2025-03-26"
		}
		req.Header.Set("Mcp-Protocol-Version", version)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		res, err := c.client.Do(req)
		if err != nil {
			c.closeErr = err
			return
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotFound {
			c.closeErr = fmt.Errorf("MCP session close: HTTP %d", res.StatusCode)
		}
	})
	return c.closeErr
}

type MCPPack struct {
	Manifest     MCPManifest
	transport    mcpTransport
	capabilities map[string]CapabilityDescription
	skills       map[string]Skill
	exposures    map[string]MCPToolExposure
	inputs       map[string]*jsonschema.Schema
	outputs      map[string]*jsonschema.Schema
}

func (p *MCPPack) Capabilities() map[string]CapabilityDescription { return p.capabilities }
func (p *MCPPack) Skills() map[string]Skill                       { return p.skills }
func (p *MCPPack) SystemPrompt() string                           { return AgentPrompt(p.Manifest.Guidance) }
func (p *MCPPack) Close() error                                   { return p.transport.Close() }
func MCPContractDigest(tool JSON) string {
	raw, _ := CanonicalJSON(tool)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func OpenMCPPack(ctx context.Context, path string, environment map[string]string) (*MCPPack, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest MCPManifest
	if err := strictUnmarshal(raw, &manifest); err != nil {
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
			_ = transport.Close()
			return nil, errors.New("duplicate capability: " + ex.Name)
		}
		seen[ex.Name] = true
		for _, skill := range ex.Skills {
			if _, ok := skills[skill]; !ok {
				_ = transport.Close()
				return nil, errors.New("unknown skill for capability: " + ex.Name)
			}
		}
		tool, ok := remote[ex.Name]
		if !ok {
			if ex.Optional {
				continue
			}
			_ = transport.Close()
			return nil, errors.New("required capability unavailable: " + ex.Name)
		}
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(ex.ContractSHA256) || MCPContractDigest(tool) != ex.ContractSHA256 {
			_ = transport.Close()
			return nil, errors.New("capability contract changed: " + ex.Name)
		}
		input, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			_ = transport.Close()
			return nil, errors.New("input schema required")
		}
		output, ok := tool["outputSchema"].(map[string]any)
		if !ok {
			_ = transport.Close()
			return nil, errors.New("structured output schema required: " + ex.Name)
		}
		iv, e := validateLocalSchema(input, false)
		if e != nil {
			_ = transport.Close()
			return nil, e
		}
		ov, e := validateLocalSchema(output, false)
		if e != nil {
			_ = transport.Close()
			return nil, e
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
		stdin, e := cmd.StdinPipe()
		if e != nil {
			return nil, nil, e
		}
		stdout, e := cmd.StdoutPipe()
		if e != nil {
			return nil, nil, e
		}
		if e := cmd.Start(); e != nil {
			return nil, nil, e
		}
		transport = &stdioMCP{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
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
		transport = &httpMCP{url: target, token: token, client: &http.Client{Timeout: time.Duration(source.TimeoutSeconds * float64(time.Second)), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	} else {
		return nil, nil, errors.New("invalid MCP transport")
	}
	initParams := JSON{"protocolVersion": "2025-03-26", "capabilities": JSON{}, "clientInfo": JSON{"name": "agenstra", "version": "1.0.0"}}
	initialized, err := transport.Request(ctx, "initialize", initParams)
	if err != nil {
		_ = transport.Close()
		return nil, nil, err
	}
	if client, ok := transport.(*httpMCP); ok {
		if version, ok := initialized["protocolVersion"].(string); ok && version != "" {
			client.protocol = version
		}
	}
	if err := transport.Notify(ctx, "notifications/initialized", JSON{}); err != nil {
		_ = transport.Close()
		return nil, nil, err
	}
	remote := map[string]JSON{}
	cursors := map[string]bool{}
	cursor := ""
	for {
		params := JSON{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		page, e := transport.Request(ctx, "tools/list", params)
		if e != nil {
			_ = transport.Close()
			return nil, nil, e
		}
		items, ok := page["tools"].([]any)
		if !ok {
			_ = transport.Close()
			return nil, nil, errors.New("invalid MCP tools list")
		}
		for _, x := range items {
			tool, ok := x.(map[string]any)
			if !ok {
				_ = transport.Close()
				return nil, nil, errors.New("invalid MCP tool")
			}
			name, _ := tool["name"].(string)
			if name == "" || remote[name] != nil {
				_ = transport.Close()
				return nil, nil, errors.New("duplicate remote capability")
			}
			remote[name] = tool
		}
		next, _ := page["nextCursor"].(string)
		if next == "" {
			break
		}
		if cursors[next] {
			_ = transport.Close()
			return nil, nil, errors.New("capability pagination repeated cursor")
		}
		cursors[next] = true
		cursor = next
	}
	return transport, remote, nil
}
