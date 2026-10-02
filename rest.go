package agenstra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var envPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
var capNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{1,127}$`)
var safeCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,120}$`)
var slotPattern = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_]*)\}`)
var headerPattern = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)
var forbiddenHeaders = map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true, "host": true, "content-length": true, "content-type": true, "transfer-encoding": true, "connection": true, "upgrade": true, "te": true, "trailer": true, "idempotency-key": true}

func checkedHeader(name string, allowIdempotency bool) error {
	if !headerPattern.MatchString(name) || (forbiddenHeaders[strings.ToLower(name)] && !(allowIdempotency && strings.EqualFold(name, "idempotency-key"))) {
		return fmt.Errorf("unsafe REST header name: %s", name)
	}
	return nil
}
func strictUnmarshal(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	return dec.Decode(v)
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
func environmentOrOS(env map[string]string) map[string]string {
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
func validateLocalSchema(schema JSON, strictREST bool) (*jsonschema.Schema, error) {
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, y := range x {
				if k == "$ref" || k == "$dynamicRef" {
					ref, ok := y.(string)
					if !ok || (strictREST && !strings.HasPrefix(ref, "#/")) || (!strictREST && !strings.HasPrefix(ref, "#")) {
						return errors.New("schema requires local references")
					}
				}
				if strictREST {
					if k == "$schema" && y != "https://json-schema.org/draft/2020-12/schema" {
						return errors.New("REST schemas require the JSON Schema 2020-12 dialect")
					}
					if k == "$id" || k == "$anchor" || k == "$dynamicAnchor" || k == "$dynamicRef" {
						return fmt.Errorf("REST schemas do not support %s", k)
					}
				}
				// Inspect schema positions, preserving ordinary property names and
				// literal JSON in enum/default/examples as provider data.
				switch k {
				case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies":
					if schemas, ok := y.(map[string]any); ok {
						for _, schema := range schemas {
							if err := walk(schema); err != nil {
								return err
							}
						}
					}
				case "allOf", "anyOf", "oneOf", "prefixItems", "items", "additionalItems", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "propertyNames", "contains", "not", "if", "then", "else", "contentSchema":
					if err := walk(y); err != nil {
						return err
					}
				}
			}
		case []any:
			for _, y := range x {
				if err := walk(y); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(schema); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource("schema.json", schema); err != nil {
		return nil, err
	}
	return compiler.Compile("schema.json")
}
func validateSchema(s *jsonschema.Schema, v any) error {
	if s == nil {
		return nil
	}
	raw, err := CanonicalJSON(v)
	if err != nil {
		return err
	}
	var normalized any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&normalized); err != nil {
		return err
	}
	return s.Validate(normalized)
}
func restPathValue(v any) (string, error) {
	raw, err := scalarValue(v)
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
func scalarValue(v any) (string, error) {
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
func safeHeaderValue(v any) (string, error) {
	s, err := scalarValue(v)
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
func traversePath(v any, path []any) (any, error) { return valueAt(v, path) }

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

func (e *RestEndpoint) UnmarshalJSON(raw []byte) error {
	type endpoint RestEndpoint
	var parsed endpoint
	if err := strictUnmarshal(raw, &parsed); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	if _, ok := keys["timeout_seconds"]; !ok {
		parsed.TimeoutSeconds = 20
	}
	*e = RestEndpoint(parsed)
	return nil
}

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
type RestPack struct {
	Manifest     RestManifest
	BaseURL      string
	Headers      map[string]string
	Client       *http.Client
	capabilities map[string]CapabilityDescription
	skills       map[string]Skill
	endpoints    map[string]RestEndpoint
	inputs       map[string]*jsonschema.Schema
	outputs      map[string]*jsonschema.Schema
	statuses     map[string]map[string]*jsonschema.Schema
}

func (p *RestPack) Capabilities() map[string]CapabilityDescription { return p.capabilities }
func (p *RestPack) Skills() map[string]Skill                       { return p.skills }
func (p *RestPack) SystemPrompt() string                           { return AgentPrompt(p.Manifest.Guidance) }
func (p *RestPack) Close() error                                   { return nil }
func (p *RestPack) ConcurrentInvocation(string) bool               { return true }
func validateRestEndpoint(e RestEndpoint, headers map[string]string) error {
	if err := validateModelOutput(e.ModelOutput); err != nil {
		return err
	}
	if err := validateModelOutputSchema(e.ModelOutput, e.OutputSchema); err != nil {
		return err
	}
	if !capNamePattern.MatchString(e.Name) || e.Description == "" || len(e.Description) > 500 {
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
		output, err := validateLocalSchema(e.OutputSchema, true)
		if err != nil || validateSchema(output, JSON{}) != nil {
			return errors.New("REST empty success output_schema must accept an empty object")
		}
		if schema, ok := e.ResponseSchemas["204"]; ok {
			validator, err := validateLocalSchema(schema, true)
			if err != nil || validateSchema(validator, JSON{}) != nil {
				return errors.New("REST 204 response_schema must accept an empty object")
			}
		}
	}
	if check := e.BusinessSuccess; check != nil {
		if len(check.Path) == 0 || len(check.Path) > 16 || !strings.HasPrefix(check.ErrorCode, "business_") || !safeCodePattern.MatchString(check.ErrorCode) || check.Value == nil {
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
	if strings.ContainsAny(slotPattern.ReplaceAllString(e.Path, ""), "{}") {
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
		if len(status) != 3 || status[0] < '1' || status[0] > '5' || !safeCodePattern.MatchString(code) {
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
	for _, match := range slotPattern.FindAllStringSubmatch(e.Path, -1) {
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

func requiredProperty(schema JSON, property string) bool {
	required, _ := schema["required"].([]any)
	for _, item := range required {
		if item == property {
			return true
		}
	}
	return false
}
func LoadRestPack(path string, environment map[string]string, client *http.Client) (*RestPack, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest RestManifest
	if err := strictUnmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != "agenstra.rest-pack.v2" || manifest.Name == "" || manifest.Version == "" || manifest.Guidance == "" || len(manifest.Capabilities) == 0 || !envPattern.MatchString(manifest.BaseURLEnv) {
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
	env := environmentOrOS(environment)
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
		if !envPattern.MatchString(variable) {
			return nil, errors.New("headers_env values must be environment variable names")
		}
		v, e := requiredEnv(env, variable)
		if e != nil {
			return nil, e
		}
		headers[name] = v
	}
	if manifest.TokenEnv != nil {
		if !envPattern.MatchString(*manifest.TokenEnv) {
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
	p := &RestPack{Manifest: manifest, BaseURL: strings.TrimRight(base, "/"), Headers: headers, Client: client, capabilities: map[string]CapabilityDescription{}, skills: skills, endpoints: map[string]RestEndpoint{}, inputs: map[string]*jsonschema.Schema{}, outputs: map[string]*jsonschema.Schema{}, statuses: map[string]map[string]*jsonschema.Schema{}}
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
		input, e := validateLocalSchema(endpoint.InputSchema, true)
		if e != nil {
			return nil, e
		}
		output, e := validateLocalSchema(endpoint.OutputSchema, true)
		if e != nil {
			return nil, e
		}
		statuses := map[string]*jsonschema.Schema{}
		for status, schema := range endpoint.ResponseSchemas {
			validator, e := validateLocalSchema(schema, true)
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
		p.capabilities[endpoint.Name] = CapabilityDescription{Name: endpoint.Name, Version: manifest.Version, Description: endpoint.Description, InputSchema: endpoint.InputSchema, OutputSchema: endpoint.OutputSchema, ModelOutput: endpoint.ModelOutput, Effect: endpoint.Effect, Replay: replay, IdempotencyArgument: endpoint.IdempotencyArgument, Operation: endpoint.Operation, SkillsList: endpoint.Skills, ApprovalRequired: endpoint.ApprovalRequired, ReferenceScope: "durable"}
	}
	return p, nil
}
func (p *RestPack) Invoke(ctx context.Context, name string, args map[string]any, inv *InvocationContext) (CapabilityResult, error) {
	endpoint, ok := p.endpoints[name]
	if !ok {
		return CapabilityResult{ErrorCode: "capability_unknown"}, nil
	}
	if validateSchema(p.inputs[name], args) != nil {
		return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
	}
	pathArgs, _ := args["path"].(map[string]any)
	queryArgs, _ := args["query"].(map[string]any)
	headersArgs, _ := args["headers"].(map[string]any)
	path := endpoint.Path
	for _, match := range slotPattern.FindAllStringSubmatch(path, -1) {
		v, e := restPathValue(pathArgs[match[1]])
		if e != nil {
			return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
		}
		path = strings.ReplaceAll(path, match[0], v)
	}
	params := url.Values{}
	for key, v := range queryArgs {
		if items, ok := v.([]any); ok {
			for _, item := range items {
				s, e := scalarValue(item)
				if e != nil {
					return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
				}
				params.Add(key, s)
			}
		} else {
			s, e := scalarValue(v)
			if e != nil {
				return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
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
			return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
		}
		headers[k] = s
	}
	if endpoint.IdempotencyHeader != nil {
		if inv == nil || inv.IdempotencyKey == "" {
			return CapabilityResult{ErrorCode: "invocation_context_required"}, nil
		}
		headers[*endpoint.IdempotencyHeader] = inv.IdempotencyKey
	}
	target := p.BaseURL + path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	var body io.Reader
	if v, ok := args["body"]; ok {
		raw, e := CanonicalJSON(v)
		if e != nil {
			return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
		}
		body = bytes.NewReader(raw)
	}
	request, e := http.NewRequestWithContext(ctx, endpoint.Method, target, body)
	if e != nil {
		return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
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
		return CapabilityResult{ErrorCode: "provider_outcome_unknown"}, nil
	}
	defer response.Body.Close()
	status := strconv.Itoa(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 500 && endpoint.Effect != "read" {
			return CapabilityResult{ErrorCode: "provider_outcome_unknown"}, nil
		}
		code := endpoint.ErrorCodes[status]
		if code == "" {
			code = "upstream_http_" + status
		}
		return CapabilityResult{ErrorCode: code}, nil
	}
	if len(endpoint.ResponseSchemas) > 0 && p.statuses[name][status] == nil {
		return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if e != nil {
		return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	var data any
	if response.StatusCode == http.StatusNoContent && len(raw) == 0 && endpoint.AllowEmptySuccess {
		data = JSON{}
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&data) != nil {
			return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
	}
	if check := endpoint.BusinessSuccess; check != nil {
		value, err := traversePath(data, check.Path)
		if err != nil {
			return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		actual, err := CanonicalJSON(value)
		if err != nil {
			return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		expected, _ := CanonicalJSON(check.Value)
		if !bytes.Equal(actual, expected) {
			return CapabilityResult{ErrorCode: check.ErrorCode}, nil
		}
	}
	data, e = traversePath(data, endpoint.ResponsePath)
	if e == nil && endpoint.ResponseMode == "wrap" {
		data = JSON{"result": data}
	}
	if e != nil || validateSchema(p.outputs[name], data) != nil {
		return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	if statusSchema := p.statuses[name][status]; statusSchema != nil && validateSchema(statusSchema, data) != nil {
		return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	object, ok := data.(map[string]any)
	if !ok {
		return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
	}
	return CapabilityResult{Data: object, ReferenceScope: "durable"}, nil
}

type RestField struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

func (f *RestField) UnmarshalJSON(raw []byte) error {
	var holder struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Required    *bool  `json:"required"`
	}
	if err := strictUnmarshal(raw, &holder); err != nil {
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

func (c *LegacyRestCapability) UnmarshalJSON(raw []byte) error {
	type capability LegacyRestCapability
	var parsed capability
	if err := strictUnmarshal(raw, &parsed); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
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

type LegacyPackManifest struct {
	Schema       string                 `json:"schema"`
	Name         string                 `json:"name"`
	Guidance     string                 `json:"guidance"`
	Capabilities []LegacyRestCapability `json:"capabilities"`
}
type legacyBinding struct {
	Definition      LegacyRestCapability
	URL             string
	Token           string
	InputSchema     JSON
	OutputSchema    JSON
	InputValidator  *jsonschema.Schema
	OutputValidator *jsonschema.Schema
}
type LegacyRestPack struct {
	Manifest     LegacyPackManifest
	Client       *http.Client
	bindings     map[string]legacyBinding
	capabilities map[string]CapabilityDescription
}

func (p *LegacyRestPack) Capabilities() map[string]CapabilityDescription { return p.capabilities }
func (p *LegacyRestPack) Skills() map[string]Skill                       { return map[string]Skill{} }
func (p *LegacyRestPack) SystemPrompt() string                           { return AgentPrompt(p.Manifest.Guidance) }
func (p *LegacyRestPack) Close() error                                   { return nil }
func fieldsSchema(fields map[string]RestField, forOutput bool, method string) (JSON, error) {
	properties := JSON{}
	required := []any{}
	for name, f := range fields {
		if !fieldPattern.MatchString(name) {
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
		fieldSchema := JSON{"type": f.Type, "description": f.Description}
		if !f.Required {
			fieldSchema["type"] = []any{f.Type, "null"}
		}
		properties[name] = fieldSchema
		if f.Required {
			required = append(required, name)
		}
	}
	schema := JSON{"type": "object", "properties": properties, "required": required}
	if !forOutput {
		schema["additionalProperties"] = false
	}
	return schema, nil
}
func LoadLegacyPack(path string, environment map[string]string, client *http.Client) (*LegacyRestPack, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := validateRawManifest(raw, nil); err != nil {
		return nil, err
	}
	var manifest LegacyPackManifest
	if err := strictUnmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != "agenstra.capability-pack.v1" || manifest.Name == "" || manifest.Guidance == "" || len(manifest.Capabilities) == 0 {
		return nil, errors.New("invalid legacy pack")
	}
	env := environmentOrOS(environment)
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	pack := &LegacyRestPack{Manifest: manifest, Client: client, bindings: map[string]legacyBinding{}, capabilities: map[string]CapabilityDescription{}}
	for _, c := range manifest.Capabilities {
		if !regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,127}$`).MatchString(c.Name) || c.Version == "" || c.Description == "" || !envPattern.MatchString(c.URLEnv) || len(c.ResponsePath) > 8 {
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
			if !envPattern.MatchString(*c.TokenEnv) {
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
		iv, err := validateLocalSchema(input, false)
		if err != nil {
			return nil, err
		}
		var output JSON
		var ov *jsonschema.Schema
		if c.Outputs != nil {
			output, err = fieldsSchema(c.Outputs, true, c.Method)
			if err != nil {
				return nil, err
			}
			ov, err = validateLocalSchema(output, false)
			if err != nil {
				return nil, err
			}
		}
		pack.bindings[c.Name] = legacyBinding{Definition: c, URL: target, Token: token, InputSchema: input, OutputSchema: output, InputValidator: iv, OutputValidator: ov}
		pack.capabilities[c.Name] = CapabilityDescription{Name: c.Name, Version: c.Version, Description: c.Description, InputSchema: input, OutputSchema: output, Effect: "read", Replay: "never", ReferenceScope: "durable"}
	}
	return pack, nil
}
func (p *LegacyRestPack) Invoke(ctx context.Context, name string, args map[string]any, _ *InvocationContext) (CapabilityResult, error) {
	binding, ok := p.bindings[name]
	if !ok {
		return CapabilityResult{ErrorCode: "capability_unknown"}, nil
	}
	if validateSchema(binding.InputValidator, args) != nil {
		return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
	}
	for key, v := range args {
		if f, ok := binding.Definition.Inputs[key]; ok {
			if f.Type == "integer" {
				if n, ok := v.(float64); ok && n != float64(int64(n)) {
					return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
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
					s, _ := scalarValue(v)
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
			payload := JSON{}
			for key, value := range args {
				if value != nil {
					payload[key] = value
				}
			}
			raw, _ := CanonicalJSON(payload)
			body = bytes.NewReader(raw)
		}
		request, err := http.NewRequestWithContext(ctx, binding.Definition.Method, target, body)
		if err != nil {
			return CapabilityResult{ErrorCode: "capability_input_invalid"}, nil
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
			return CapabilityResult{ErrorCode: "upstream_unavailable"}, nil
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
		response.Body.Close()
		if response.StatusCode >= 400 {
			if attempt+1 < binding.Definition.MaxAttempts && (response.StatusCode == 429 || response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504) {
				continue
			}
			return CapabilityResult{ErrorCode: fmt.Sprintf("upstream_http_%d", response.StatusCode)}, nil
		}
		if readErr != nil {
			return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		var data any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&data) != nil {
			return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		data, err = traversePath(data, binding.Definition.ResponsePath)
		if err != nil {
			return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		object, ok := data.(map[string]any)
		if !ok {
			return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
		}
		if binding.OutputValidator != nil {
			if validateSchema(binding.OutputValidator, object) != nil {
				return CapabilityResult{ErrorCode: "upstream_response_invalid"}, nil
			}
			filtered := JSON{}
			for key := range binding.Definition.Outputs {
				if v, ok := object[key]; ok && v != nil {
					filtered[key] = v
				}
			}
			object = filtered
		}
		return CapabilityResult{Data: object, ReferenceScope: "durable"}, nil
	}
	return CapabilityResult{ErrorCode: "upstream_unavailable"}, nil
}
