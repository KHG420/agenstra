package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// EnvPattern validates declared environment reference names.
var EnvPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// CapNamePattern validates published capability names.
var CapNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{1,127}$`)

var headerPattern = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

var forbiddenHeaders = map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true, "host": true, "content-length": true, "content-type": true, "transfer-encoding": true, "connection": true, "upgrade": true, "te": true, "trailer": true, "idempotency-key": true}

func checkedHeader(name string, allowIdempotency bool) error {
	if !headerPattern.MatchString(name) || (forbiddenHeaders[strings.ToLower(name)] && !(allowIdempotency && strings.EqualFold(name, "idempotency-key"))) {
		return fmt.Errorf("unsafe REST header name: %s", name)
	}
	return nil
}

func requiredEnv(env map[string]string, name string) (string, error) {
	v := strings.TrimSpace(env[name])
	if v == "" {
		return "", fmt.Errorf("missing or invalid environment variable: %s", name)
	}
	for _, c := range v {
		if c < 32 || c == 127 {
			return "", fmt.Errorf("missing or invalid environment variable: %s", name)
		}
	}
	return v, nil
}

// EnvironmentOrOS uses the explicit environment when supplied and otherwise reads the process environment.
func EnvironmentOrOS(env map[string]string) map[string]string {
	if env != nil {
		return env
	}
	result := map[string]string{}
	for _, kv := range os.Environ() {
		p := strings.SplitN(kv, "=", 2)
		result[p[0]] = p[1]
	}
	return result
}

func restPathValue(v any) (string, error) {
	raw, err := agentcontract.ScalarValue(v)
	if err != nil || raw == "" || raw == "." || raw == ".." || strings.ContainsAny(raw, "/\\%?#") {
		return "", errors.New("unsafe path parameter")
	}
	for _, c := range raw {
		if c < 32 || c == 127 {
			return "", errors.New("unsafe path parameter")
		}
	}
	return url.PathEscape(raw), nil
}

func safeHeaderValue(v any) (string, error) {
	s, err := agentcontract.ScalarValue(v)
	if err != nil {
		return "", err
	}
	for _, c := range s {
		if c < 32 || c == 127 {
			return "", errors.New("unsafe header value")
		}
	}
	return s, nil
}

func traversePath(v any, path []any) (any, error) { return agentcontract.ValueAt(v, path) }

// RestPack is an opened REST provider with compiled request and response contracts.
// Catalogs and configuration are read-only during invocation; the HTTP client remains caller-owned.
type RestPack struct {
	Manifest     agentcontract.RestManifest
	BaseURL      string
	Headers      map[string]string
	Client       *http.Client
	capabilities map[string]agentcontract.CapabilityDescription
	skills       map[string]agentcontract.Skill
	endpoints    map[string]agentcontract.RestEndpoint
	inputs       map[string]*jsonschema.Schema
	outputs      map[string]*jsonschema.Schema
	statuses     map[string]map[string]*jsonschema.Schema
}

// Capabilities returns the read-only capability catalog; callers must not mutate it.
func (p *RestPack) Capabilities() map[string]agentcontract.CapabilityDescription {
	return p.capabilities
}

// Skills returns read-only pinned usage guides; callers must not mutate the map.
func (p *RestPack) Skills() map[string]agentcontract.Skill { return p.skills }

// SystemPrompt returns fixed usage guidance without connection credentials.
func (p *RestPack) SystemPrompt() string { return AgentPrompt(p.Manifest.Guidance) }

// Close is a no-op; REST providers do not own the supplied HTTP client or transport.
func (p *RestPack) Close() error { return nil }

// ConcurrentInvocation declares that REST calls and request validation support parallel reads.
func (p *RestPack) ConcurrentInvocation(string) bool { return true }

func validateRestEndpoint(e agentcontract.RestEndpoint, headers map[string]string) error {
	if err := agentcontract.ValidateModelOutput(e.ModelOutput); err != nil {
		return err
	}
	if err := agentcontract.ValidateModelOutputSchema(e.ModelOutput, e.OutputSchema); err != nil {
		return err
	}
	if !CapNamePattern.MatchString(e.Name) || e.Description == "" || len(e.Description) > 500 {
		return errors.New("invalid REST capability")
	}
	if !map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}[e.Method] {
		return errors.New("invalid REST method")
	}
	if !map[string]bool{"read": true, "compute": true, "write": true, "destructive": true}[e.Effect] {
		return errors.New("invalid REST effect")
	}
	if len(e.ResponsePath) > 16 {
		return errors.New("REST response_path is too long")
	}
	if e.ResponseMode != "" && e.ResponseMode != "wrap" {
		return errors.New("REST response_mode must be wrap")
	}
	if e.ResponseMode == "wrap" {
		props, _ := e.OutputSchema["properties"].(map[string]any)
		if e.OutputSchema["type"] != "object" || props["result"] == nil || !requiredProperty(e.OutputSchema, "result") {
			return errors.New("REST wrapped output_schema must require result")
		}
	}
	if e.AllowEmptySuccess {
		if len(e.ResponsePath) > 0 {
			return errors.New("REST empty success cannot use response_path")
		}
		output, err := agentcontract.ValidateLocalSchema(e.OutputSchema, true)
		if err != nil || agentcontract.ValidateSchema(output, agentcontract.JSON{}) != nil {
			return errors.New("REST empty success output_schema must accept an empty object")
		}
		if schema, ok := e.ResponseSchemas["204"]; ok {
			validator, err := agentcontract.ValidateLocalSchema(schema, true)
			if err != nil || agentcontract.ValidateSchema(validator, agentcontract.JSON{}) != nil {
				return errors.New("REST 204 response_schema must accept an empty object")
			}
		}
	}
	if check := e.BusinessSuccess; check != nil {
		if len(check.Path) == 0 || len(check.Path) > 16 || !strings.HasPrefix(check.ErrorCode, "business_") || !agentcontract.SafeCodePattern.MatchString(check.ErrorCode) || check.Value == nil {
			return errors.New("invalid REST business_success")
		}
		for _, part := range check.Path {
			switch v := part.(type) {
			case string:
				if v == "" || len(v) > 128 {
					return errors.New("invalid REST business_success path")
				}
			case json.Number:
				n, err := v.Int64()
				if err != nil || n < 0 || n > 1000000 {
					return errors.New("invalid REST business_success path")
				}
			default:
				return errors.New("invalid REST business_success path")
			}
		}
		switch check.Value.(type) {
		case string, bool, json.Number:
		default:
			return errors.New("REST business_success value must be a scalar")
		}
	}
	if e.TimeoutSeconds < 0 || e.TimeoutSeconds > 300 {
		return errors.New("invalid REST timeout_seconds")
	}
	if !strings.HasPrefix(e.Path, "/") || strings.ContainsAny(e.Path, "?\\#%") || strings.Contains(e.Path, "//") {
		return errors.New("REST path contains unsafe syntax")
	}
	for _, seg := range strings.Split(e.Path[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("REST path contains empty or traversal segment")
		}
	}
	if strings.ContainsAny(agentcontract.SlotPattern.ReplaceAllString(e.Path, ""), "{}") {
		return errors.New("REST path has an invalid template slot")
	}
	if e.IdempotencyHeader != nil {
		if err := checkedHeader(*e.IdempotencyHeader, true); err != nil {
			return err
		}
	}
	if e.IdempotencyArgument != nil && len(e.IdempotencyArgument) == 0 {
		return errors.New("idempotency_argument must identify an input field")
	}
	for status, code := range e.ErrorCodes {
		if len(status) != 3 || status[0] < '1' || status[0] > '5' || !agentcontract.SafeCodePattern.MatchString(code) {
			return errors.New("error_codes must map HTTP status to safe error codes")
		}
	}
	for status := range e.ResponseSchemas {
		if len(status) != 3 || status[0] != '2' {
			return errors.New("response_schemas must use successful HTTP statuses")
		}
	}
	input := e.InputSchema
	if input["type"] != "object" || input["additionalProperties"] != false {
		return fmt.Errorf("%s: input_schema must be a closed object", e.Name)
	}
	props, ok := input["properties"].(map[string]any)
	if !ok {
		return errors.New("REST input properties must be object")
	}
	for k := range props {
		if k != "path" && k != "query" && k != "body" && k != "headers" {
			return errors.New("REST input sections must be path/query/body/headers")
		}
	}
	slots := map[string]bool{}
	for _, match := range agentcontract.SlotPattern.FindAllStringSubmatch(e.Path, -1) {
		slots[match[1]] = true
	}
	pathProps := map[string]any{}
	pathRequired := map[string]bool{}
	if p, ok := props["path"].(map[string]any); ok {
		pathProps, _ = p["properties"].(map[string]any)
		if pathProps == nil {
			pathProps = map[string]any{}
		}
		if req, ok := p["required"].([]any); ok {
			for _, x := range req {
				if s, ok := x.(string); ok {
					pathRequired[s] = true
				}
			}
		}
	}
	if len(slots) != len(pathProps) || len(slots) != len(pathRequired) {
		return errors.New("path schema must require exactly its template slots")
	}
	for s := range slots {
		if _, ok := pathProps[s]; !ok || !pathRequired[s] {
			return errors.New("path schema must require exactly its template slots")
		}
	}
	if len(slots) > 0 {
		required := false
		if req, ok := input["required"].([]any); ok {
			for _, x := range req {
				if x == "path" {
					required = true
				}
			}
		}
		if !required {
			return errors.New("path section must be required")
		}
	}
	for _, section := range []string{"path", "query", "headers"} {
		if v, ok := props[section]; ok {
			part, ok := v.(map[string]any)
			if !ok || part["type"] != "object" || part["additionalProperties"] != false {
				return errors.New(section + " must be a closed object")
			}
		}
	}
	if h, ok := props["headers"].(map[string]any); ok {
		if hp, ok := h["properties"].(map[string]any); ok {
			for key := range hp {
				if err := checkedHeader(key, false); err != nil {
					return err
				}
				for static := range headers {
					if strings.EqualFold(key, static) {
						return errors.New("dynamic header conflicts with credential header")
					}
				}
				if e.IdempotencyHeader != nil && strings.EqualFold(key, *e.IdempotencyHeader) {
					return errors.New("idempotency header is host supplied")
				}
			}
		}
	}
	if e.Method == "GET" {
		if _, ok := props["body"]; ok {
			return errors.New("GET cannot declare a JSON body")
		}
	}
	return nil
}

func requiredProperty(schema agentcontract.JSON, property string) bool {
	required, _ := schema["required"].([]any)
	for _, item := range required {
		if item == property {
			return true
		}
	}
	return false
}

// LoadRestPack validates a manifest and resolves its configured connection references.
func LoadRestPack(path string, environment map[string]string, client *http.Client) (*RestPack, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest agentcontract.RestManifest
	if err := jsonvalue.DecodeStrict(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != "agenstra.rest-pack.v2" || manifest.Name == "" || manifest.Version == "" || manifest.Guidance == "" || len(manifest.Capabilities) == 0 || !EnvPattern.MatchString(manifest.BaseURLEnv) {
		return nil, errors.New("invalid REST pack manifest")
	}
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
	env := EnvironmentOrOS(environment)
	base, err := requiredEnv(env, manifest.BaseURLEnv)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("REST base URL must be an HTTP(S) origin or fixed base path")
	}
	for _, seg := range strings.Split(parsed.Path, "/") {
		if seg == "." || seg == ".." {
			return nil, errors.New("REST base URL contains traversal path")
		}
	}
	headers := map[string]string{}
	for name, variable := range manifest.HeadersEnv {
		if err := checkedHeader(name, false); err != nil {
			return nil, err
		}
		if !EnvPattern.MatchString(variable) {
			return nil, errors.New("headers_env values must be environment variable names")
		}
		v, e := requiredEnv(env, variable)
		if e != nil {
			return nil, e
		}
		headers[name] = v
	}
	if manifest.TokenEnv != nil {
		if !EnvPattern.MatchString(*manifest.TokenEnv) {
			return nil, errors.New("invalid token_env")
		}
		token, e := requiredEnv(env, *manifest.TokenEnv)
		if e != nil {
			return nil, e
		}
		headers["Authorization"] = "Bearer " + token
	}
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	p := &RestPack{Manifest: manifest, BaseURL: strings.TrimRight(base, "/"), Headers: headers, Client: client, capabilities: map[string]agentcontract.CapabilityDescription{}, skills: skills, endpoints: map[string]agentcontract.RestEndpoint{}, inputs: map[string]*jsonschema.Schema{}, outputs: map[string]*jsonschema.Schema{}, statuses: map[string]map[string]*jsonschema.Schema{}}
	for _, endpoint := range manifest.Capabilities {
		if _, ok := p.endpoints[endpoint.Name]; ok {
			return nil, fmt.Errorf("duplicate REST capability: %s", endpoint.Name)
		}
		if err := validateRestEndpoint(endpoint, headers); err != nil {
			return nil, err
		}
		for _, name := range endpoint.Skills {
			if _, ok := skills[name]; !ok {
				return nil, errors.New("unknown skill for REST capability")
			}
		}
		input, e := agentcontract.ValidateLocalSchema(endpoint.InputSchema, true)
		if e != nil {
			return nil, e
		}
		output, e := agentcontract.ValidateLocalSchema(endpoint.OutputSchema, true)
		if e != nil {
			return nil, e
		}
		statuses := map[string]*jsonschema.Schema{}
		for status, schema := range endpoint.ResponseSchemas {
			validator, e := agentcontract.ValidateLocalSchema(schema, true)
			if e != nil {
				return nil, e
			}
			statuses[status] = validator
		}
		replay := "never"
		if endpoint.Method == "GET" && endpoint.Effect == "read" {
			replay = "safe"
		} else if endpoint.IdempotencyHeader != nil || endpoint.IdempotencyArgument != nil {
			replay = "idempotent"
		}
		p.endpoints[endpoint.Name] = endpoint
		p.inputs[endpoint.Name] = input
		p.outputs[endpoint.Name] = output
		p.statuses[endpoint.Name] = statuses
		p.capabilities[endpoint.Name] = agentcontract.CapabilityDescription{Name: endpoint.Name, Version: manifest.Version, Description: endpoint.Description, InputSchema: endpoint.InputSchema, OutputSchema: endpoint.OutputSchema, ModelOutput: endpoint.ModelOutput, Effect: endpoint.Effect, Replay: replay, IdempotencyArgument: endpoint.IdempotencyArgument, Operation: endpoint.Operation, SkillsList: endpoint.Skills, ApprovalRequired: endpoint.ApprovalRequired, ReferenceScope: "durable"}
	}
	return p, nil
}

// Invoke validates and executes the selected capability with request cancellation.
// Provider failures use the structured ErrorCode channel when their outcome is known.
func (p *RestPack) Invoke(ctx context.Context, name string, args map[string]any, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
	endpoint, ok := p.endpoints[name]
	if !ok {
		return agentcontract.CapabilityResult{ErrorCode: "capability_unknown"}, nil
	}
	if agentcontract.ValidateSchema(p.inputs[name], args) != nil {
		return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
	}
	pathArgs, _ := args["path"].(map[string]any)
	queryArgs, _ := args["query"].(map[string]any)
	headersArgs, _ := args["headers"].(map[string]any)
	path := endpoint.Path
	for _, match := range agentcontract.SlotPattern.FindAllStringSubmatch(path, -1) {
		v, e := restPathValue(pathArgs[match[1]])
		if e != nil {
			return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
		}
		path = strings.ReplaceAll(path, match[0], v)
	}
	params := url.Values{}
	for key, v := range queryArgs {
		if items, ok := v.([]any); ok {
			for _, item := range items {
				s, e := agentcontract.ScalarValue(item)
				if e != nil {
					return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
				}
				params.Add(key, s)
			}
		} else {
			s, e := agentcontract.ScalarValue(v)
			if e != nil {
				return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
			}
			params.Add(key, s)
		}
	}
	headers := map[string]string{}
	for k, v := range p.Headers {
		headers[k] = v
	}
	for k, v := range headersArgs {
		s, e := safeHeaderValue(v)
		if e != nil {
			return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
		}
		headers[k] = s
	}
	if endpoint.IdempotencyHeader != nil {
		if inv == nil || inv.IdempotencyKey == "" {
			return agentcontract.CapabilityResult{ErrorCode: "invocation_context_required"}, nil
		}
		headers[*endpoint.IdempotencyHeader] = inv.IdempotencyKey
	}
	target := p.BaseURL + path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	var body io.Reader
	if v, ok := args["body"]; ok {
		raw, e := agentcontract.CanonicalJSON(v)
		if e != nil {
			return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
		}
		body = bytes.NewReader(raw)
	}
	request, e := http.NewRequestWithContext(ctx, endpoint.Method, target, body)
	if e != nil {
		return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	client := *p.Client
	if endpoint.TimeoutSeconds <= 0 {
		endpoint.TimeoutSeconds = 20
	}
	client.Timeout = time.Duration(endpoint.TimeoutSeconds * float64(time.Second))
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, e := client.Do(request)
	if e != nil {
		return agentcontract.CapabilityResult{ErrorCode: "provider_outcome_unknown"}, nil
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			log.Print("HTTP response cleanup failed")
		}
	}()
	status := strconv.Itoa(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 500 && endpoint.Effect != "read" {
			return agentcontract.CapabilityResult{ErrorCode: "provider_outcome_unknown"}, nil
		}
		code := endpoint.ErrorCodes[status]
		if code == "" {
			code = "upstream_http_" + status
		}
		return agentcontract.CapabilityResult{ErrorCode: code}, nil
	}
	if len(endpoint.ResponseSchemas) > 0 && p.statuses[name][status] == nil {
		return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if e != nil || len(raw) > 16<<20 {
		return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	var data any
	if response.StatusCode == http.StatusNoContent && len(raw) == 0 && endpoint.AllowEmptySuccess {
		data = agentcontract.JSON{}
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if !json.Valid(raw) || dec.Decode(&data) != nil {
			return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
	}
	if check := endpoint.BusinessSuccess; check != nil {
		value, err := traversePath(data, check.Path)
		if err != nil {
			return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		schema, err := agentcontract.ValidateLocalSchema(agentcontract.JSON{"const": check.Value}, false)
		if err != nil {
			return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}

		// Responses already use JSON types and json.Number. Validate directly
		// so canonical float formatting cannot round the business status value.
		if schema.Validate(value) != nil {
			return agentcontract.CapabilityResult{ErrorCode: check.ErrorCode}, nil
		}
	}
	data, e = traversePath(data, endpoint.ResponsePath)
	if e == nil && endpoint.ResponseMode == "wrap" {
		data = agentcontract.JSON{"result": data}
	}
	if e != nil || agentcontract.ValidateSchema(p.outputs[name], data) != nil {
		return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	if statusSchema := p.statuses[name][status]; statusSchema != nil && agentcontract.ValidateSchema(statusSchema, data) != nil {
		return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	object, ok := data.(map[string]any)
	if !ok {
		return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	return agentcontract.CapabilityResult{Data: object, ReferenceScope: "durable"}, nil
}

type legacyBinding struct {
	Definition      agentcontract.LegacyRestCapability
	URL             string
	Token           string
	InputSchema     agentcontract.JSON
	OutputSchema    agentcontract.JSON
	InputValidator  *jsonschema.Schema
	OutputValidator *jsonschema.Schema
}

// LegacyRestPack adapts a validated legacy pack to the provider contract.
// Its configuration and catalogs must remain read-only during invocation.
type LegacyRestPack struct {
	Manifest     agentcontract.LegacyPackManifest
	Client       *http.Client
	bindings     map[string]legacyBinding
	capabilities map[string]agentcontract.CapabilityDescription
}

// Capabilities returns the read-only capability catalog; callers must not mutate it.
func (p *LegacyRestPack) Capabilities() map[string]agentcontract.CapabilityDescription {
	return p.capabilities
}

// Skills returns an empty catalog because legacy packs do not declare pinned guides.
func (p *LegacyRestPack) Skills() map[string]agentcontract.Skill {
	return map[string]agentcontract.Skill{}
}

// SystemPrompt returns fixed usage guidance without connection credentials.
func (p *LegacyRestPack) SystemPrompt() string { return AgentPrompt(p.Manifest.Guidance) }

// Close is a no-op; legacy REST providers do not own the supplied HTTP client or transport.
func (p *LegacyRestPack) Close() error { return nil }

func fieldsSchema(fields map[string]agentcontract.RestField, forOutput bool, method string) (agentcontract.JSON, error) {
	properties := agentcontract.JSON{}
	required := []any{}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := fields[name]
		if !agentcontract.FieldPattern.MatchString(name) {
			return nil, errors.New("invalid field name")
		}
		if f.Description == "" || len(f.Description) > 300 {
			return nil, errors.New("invalid field description")
		}
		if !map[string]bool{"string": true, "integer": true, "number": true, "boolean": true, "object": true, "array": true}[f.Type] {
			return nil, errors.New("invalid field type")
		}
		if !forOutput && method == "GET" && (f.Type == "object" || f.Type == "array") {
			return nil, errors.New("GET inputs must be scalar")
		}
		fieldSchema := agentcontract.JSON{"type": f.Type, "description": f.Description}
		if !f.Required {
			fieldSchema["type"] = []any{f.Type, "null"}
		}
		properties[name] = fieldSchema
		if f.Required {
			required = append(required, name)
		}
	}
	schema := agentcontract.JSON{"type": "object", "properties": properties, "required": required}
	if !forOutput {
		schema["additionalProperties"] = false
	}
	return schema, nil
}

// LoadLegacyPack validates and opens a legacy pack without taking ownership of a supplied HTTP client.
func LoadLegacyPack(path string, environment map[string]string, client *http.Client) (*LegacyRestPack, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := validateRawManifest(raw, nil); err != nil {
		return nil, err
	}
	var manifest agentcontract.LegacyPackManifest
	if err := jsonvalue.DecodeStrict(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != "agenstra.capability-pack.v1" || manifest.Name == "" || manifest.Guidance == "" || len(manifest.Capabilities) == 0 {
		return nil, errors.New("invalid legacy pack")
	}
	env := EnvironmentOrOS(environment)
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	pack := &LegacyRestPack{Manifest: manifest, Client: client, bindings: map[string]legacyBinding{}, capabilities: map[string]agentcontract.CapabilityDescription{}}
	for _, c := range manifest.Capabilities {
		if !regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,127}$`).MatchString(c.Name) || c.Version == "" || c.Description == "" || !EnvPattern.MatchString(c.URLEnv) || len(c.ResponsePath) > 8 {
			return nil, errors.New("invalid legacy capability")
		}
		if c.Method == "" {
			c.Method = "POST"
		}
		if c.Method != "GET" && c.Method != "POST" {
			return nil, errors.New("invalid legacy method")
		}
		if c.MaxAttempts == 0 {
			c.MaxAttempts = 1
		}
		if c.MaxAttempts < 1 || c.MaxAttempts > 3 {
			return nil, errors.New("invalid max_attempts")
		}
		if c.TimeoutSeconds == 0 {
			c.TimeoutSeconds = 20
		}
		if c.TimeoutSeconds <= 0 || c.TimeoutSeconds > 300 {
			return nil, errors.New("invalid timeout_seconds")
		}
		if _, exists := pack.bindings[c.Name]; exists {
			return nil, errors.New("duplicate capability name")
		}
		target, err := requiredEnv(env, c.URLEnv)
		if err != nil {
			return nil, err
		}
		parsed, err := url.Parse(target)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, errors.New("invalid URL environment variable")
		}
		token := ""
		if c.TokenEnv != nil {
			if !EnvPattern.MatchString(*c.TokenEnv) {
				return nil, errors.New("invalid token_env")
			}
			token, err = requiredEnv(env, *c.TokenEnv)
			if err != nil {
				return nil, err
			}
		}
		input, err := fieldsSchema(c.Inputs, false, c.Method)
		if err != nil {
			return nil, err
		}
		iv, err := agentcontract.ValidateLocalSchema(input, false)
		if err != nil {
			return nil, err
		}
		var output agentcontract.JSON
		var ov *jsonschema.Schema
		if c.Outputs != nil {
			output, err = fieldsSchema(c.Outputs, true, c.Method)
			if err != nil {
				return nil, err
			}
			ov, err = agentcontract.ValidateLocalSchema(output, false)
			if err != nil {
				return nil, err
			}
		}
		pack.bindings[c.Name] = legacyBinding{Definition: c, URL: target, Token: token, InputSchema: input, OutputSchema: output, InputValidator: iv, OutputValidator: ov}
		pack.capabilities[c.Name] = agentcontract.CapabilityDescription{Name: c.Name, Version: c.Version, Description: c.Description, InputSchema: input, OutputSchema: output, Effect: "read", Replay: "never", ReferenceScope: "durable"}
	}
	return pack, nil
}

// Invoke validates and executes the selected capability with request cancellation.
// Provider failures use the structured ErrorCode channel when their outcome is known.
func (p *LegacyRestPack) Invoke(ctx context.Context, name string, args map[string]any, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
	binding, ok := p.bindings[name]
	if !ok {
		return agentcontract.CapabilityResult{ErrorCode: "capability_unknown"}, nil
	}
	if agentcontract.ValidateSchema(binding.InputValidator, args) != nil {
		return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
	}
	for key, v := range args {
		if f, ok := binding.Definition.Inputs[key]; ok {
			if f.Type == "integer" {
				if n, ok := v.(float64); ok && n != float64(int64(n)) {
					return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
				}
			}
		}
	}
	client := *p.Client
	client.Timeout = time.Duration(binding.Definition.TimeoutSeconds * float64(time.Second))
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for attempt := 0; attempt < binding.Definition.MaxAttempts; attempt++ {
		target := binding.URL
		var body io.Reader
		if binding.Definition.Method == "GET" {
			query := url.Values{}
			for key, v := range args {
				if v != nil {
					s, err := agentcontract.ScalarValue(v)
					if err != nil {
						return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
					}
					query.Set(key, s)
				}
			}
			if len(query) > 0 {
				separator := "?"
				if strings.Contains(target, "?") {
					separator = "&"
				}
				target += separator + query.Encode()
			}
		} else {
			payload := agentcontract.JSON{}
			for key, value := range args {
				if value != nil {
					payload[key] = value
				}
			}
			raw, err := agentcontract.CanonicalJSON(payload)
			if err != nil {
				return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
			}
			body = bytes.NewReader(raw)
		}
		request, err := http.NewRequestWithContext(ctx, binding.Definition.Method, target, body)
		if err != nil {
			return agentcontract.CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if binding.Token != "" {
			request.Header.Set("Authorization", "Bearer "+binding.Token)
		}
		response, err := client.Do(request)
		if err != nil {
			if attempt+1 < binding.Definition.MaxAttempts {
				continue
			}
			return agentcontract.CapabilityResult{ErrorCode: "upstream_unavailable"}, nil
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
		if err := response.Body.Close(); err != nil {
			log.Print("HTTP response cleanup failed")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			if attempt+1 < binding.Definition.MaxAttempts && (response.StatusCode == 429 || response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504) {
				continue
			}
			return agentcontract.CapabilityResult{ErrorCode: fmt.Sprintf("upstream_http_%d", response.StatusCode)}, nil
		}
		if readErr != nil || len(raw) > 16<<20 {
			return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		var data any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if !json.Valid(raw) || dec.Decode(&data) != nil {
			return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		data, err = traversePath(data, binding.Definition.ResponsePath)
		if err != nil {
			return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		object, ok := data.(map[string]any)
		if !ok {
			return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		if binding.OutputValidator != nil {
			if agentcontract.ValidateSchema(binding.OutputValidator, object) != nil {
				return agentcontract.CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
			}
			filtered := agentcontract.JSON{}
			for key := range binding.Definition.Outputs {
				if v, ok := object[key]; ok && v != nil {
					filtered[key] = v
				}
			}
			object = filtered
		}
		return agentcontract.CapabilityResult{Data: object, ReferenceScope: "durable"}, nil
	}
	return agentcontract.CapabilityResult{ErrorCode: "upstream_unavailable"}, nil
}
