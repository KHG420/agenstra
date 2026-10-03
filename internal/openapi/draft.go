// Package openapi converts selected OpenAPI operations into REST manifest drafts.
package openapi

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/KHG420/agenstra/internal/jsonvalue"
)

var slotPattern = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_]*)\}`)

// Draft validates selected operations and returns a REST manifest draft for review.
func Draft(doc map[string]any, name, baseURLEnv string, operations []string, effects map[string]string, tokenEnv string) (map[string]any, error) {
	version, _ := doc["openapi"].(string)
	if !strings.HasPrefix(version, "3.0.") && !strings.HasPrefix(version, "3.1.") {
		return nil, errors.New("only OpenAPI 3.0/3.1 JSON documents are supported")
	}
	if len(operations) == 0 {
		return nil, errors.New("select distinct operationIds with --operation")
	}
	selected := map[string]bool{}
	for _, op := range operations {
		if selected[op] {
			return nil, errors.New("select distinct operationIds with --operation")
		}
		selected[op] = true
	}
	for op, effect := range effects {
		if !selected[op] {
			return nil, errors.New("--effect references an operationId that was not selected")
		}
		if !map[string]bool{"read": true, "compute": true, "write": true, "destructive": true}[effect] {
			return nil, errors.New("effect must be read, compute, write, or destructive")
		}
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		return nil, errors.New("OpenAPI paths must be an object")
	}
	found := map[string]map[string]any{}
	for path, rawItem := range paths {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, errors.New("OpenAPI path item is invalid")
		}
		for method, rawOp := range item {
			if !map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}[method] {
				continue
			}
			operation, ok := rawOp.(map[string]any)
			if !ok {
				continue
			}
			opID, _ := operation["operationId"].(string)
			if !selected[opID] {
				continue
			}
			if found[opID] != nil {
				return nil, fmt.Errorf("duplicate operationId: %s", opID)
			}
			security := operation["security"]
			if security == nil {
				security = doc["security"]
			}
			if security != nil {
				if _, ok := security.([]any); !ok {
					return nil, fmt.Errorf("%s: unsupported security declaration", opID)
				}
			}
			if requirements, ok := security.([]any); ok && len(requirements) > 0 {
				if tokenEnv == "" || len(requirements) != 1 {
					return nil, fmt.Errorf("%s: security needs explicit --token-env for one bearer scheme", opID)
				}
				req, ok := requirements[0].(map[string]any)
				if !ok || len(req) != 1 {
					return nil, errors.New("unsupported security requirements")
				}
				for schemeName, scopes := range req {
					arr, ok := scopes.([]any)
					if !ok || len(arr) != 0 {
						return nil, errors.New("unsupported security scheme")
					}
					components, _ := doc["components"].(map[string]any)
					schemes, _ := components["securitySchemes"].(map[string]any)
					scheme, e := resolveOpenAPI(doc, schemes[schemeName], map[string]bool{})
					if e != nil || scheme["type"] != "http" || strings.ToLower(fmt.Sprint(scheme["scheme"])) != "bearer" {
						return nil, errors.New("unsupported security scheme")
					}
				}
			}
			parameterMap := map[string]map[string]any{}
			for _, src := range []any{item["parameters"], operation["parameters"]} {
				if src == nil {
					src = []any{}
				}
				group, e := resolveParameters(doc, src)
				if e != nil {
					return nil, e
				}
				within := map[string]bool{}
				for _, p := range group {
					key := fmt.Sprint(p["in"]) + "/" + fmt.Sprint(p["name"])
					if within[key] {
						return nil, fmt.Errorf("%s: duplicate parameter %s", opID, key)
					}
					within[key] = true
					parameterMap[key] = p
				}
			}
			byLocation := map[string]map[string]any{"path": {}, "query": {}}
			required := map[string][]any{"path": {}, "query": {}}
			for _, p := range parameterMap {
				location, _ := p["in"].(string)
				key, _ := p["name"].(string)
				if key == "" || (location != "path" && location != "query") {
					return nil, fmt.Errorf("%s: only path and query parameters are supported", opID)
				}
				if p["content"] != nil {
					return nil, errors.New("parameter content serialization is unsupported")
				}
				style, _ := p["style"].(string)
				if style == "" {
					if location == "path" {
						style = "simple"
					} else {
						style = "form"
					}
				}
				explode, hasExplode := p["explode"].(bool)
				if !hasExplode {
					explode = style == "form"
				}
				schema, ok := p["schema"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s: parameter %s needs a schema", opID, key)
				}
				if location == "path" && (style != "simple" || explode) {
					return nil, errors.New("path parameters require simple serialization")
				}
				if location == "query" && (style != "form" || !explode) {
					return nil, errors.New("query parameters require form/explode=true")
				}
				if location == "path" && (schema["type"] == "object" || schema["type"] == "array") {
					return nil, errors.New("complex path parameters are unsupported")
				}
				if location == "query" && schema["type"] == "object" {
					return nil, errors.New("object query parameters are unsupported")
				}
				if location == "query" && schema["type"] == "array" {
					items, _ := schema["items"].(map[string]any)
					if !map[string]bool{"string": true, "integer": true, "number": true, "boolean": true}[fmt.Sprint(items["type"])] {
						return nil, errors.New("query arrays require scalar items")
					}
				}
				byLocation[location][key] = schema
				if p["required"] == true || location == "path" {
					required[location] = append(required[location], key)
				}
			}
			slots := map[string]bool{}
			for _, m := range slotPattern.FindAllStringSubmatch(path, -1) {
				slots[m[1]] = true
			}
			if len(slots) != len(byLocation["path"]) {
				return nil, errors.New("path parameters do not match the path template")
			}
			for key := range slots {
				if _, ok := byLocation["path"][key]; !ok {
					return nil, errors.New("path parameters do not match the path template")
				}
			}
			props := map[string]any{}
			rootRequired := []any{}
			for _, loc := range []string{"path", "query"} {
				if len(byLocation[loc]) > 0 {
					props[loc] = map[string]any{"type": "object", "properties": byLocation[loc], "required": required[loc], "additionalProperties": false}
					if len(required[loc]) > 0 {
						rootRequired = append(rootRequired, loc)
					}
				}
			}
			if source := operation["requestBody"]; source != nil {
				body, e := resolveOpenAPI(doc, source, map[string]bool{})
				if e != nil {
					return nil, e
				}
				schema, e := jsonMedia(doc, body, opID+" request body")
				if e != nil {
					return nil, e
				}
				props["body"] = schema
				if body["required"] == true {
					rootRequired = append(rootRequired, "body")
				}
				if method == "get" {
					return nil, errors.New("GET request body is unsupported")
				}
			}
			input, e := convertOpenAPISchema(doc, map[string]any{"type": "object", "properties": props})
			if e != nil {
				return nil, e
			}
			input["required"] = rootRequired
			input["additionalProperties"] = false
			responses, ok := operation["responses"].(map[string]any)
			if !ok {
				return nil, errors.New("responses must be an object")
			}
			byStatus := map[string]any{}
			wrapResponse := false
			emptySuccess := false
			for status, source := range responses {
				if regexp.MustCompile(`^2[0-9]{2}$`).MatchString(status) {
					resolved, e := resolveOpenAPI(doc, source, map[string]bool{})
					if e != nil {
						return nil, e
					}
					if status == "204" && resolved["content"] == nil {
						byStatus[status] = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
						emptySuccess = true
						continue
					}
					schema, e := jsonMedia(doc, resolved, opID+" response "+status)
					if e != nil {
						return nil, e
					}
					converted, e := convertOpenAPISchema(doc, schema)
					if e != nil {
						return nil, e
					}
					converted, wrapped := openAPIOutputSchema(converted)
					wrapResponse = wrapResponse || wrapped
					byStatus[status] = converted
				}
			}
			if len(byStatus) == 0 {
				return nil, errors.New("a JSON 2xx response schema is required")
			}
			var first map[string]any
			for _, v := range byStatus {
				first = v.(map[string]any)
				break
			}
			firstRaw, err := jsonvalue.Canonical(first)
			if err != nil {
				return nil, err
			}
			for _, v := range byStatus {
				raw, err := jsonvalue.Canonical(v)
				if err != nil {
					return nil, err
				}
				if !bytes.Equal(raw, firstRaw) {
					return nil, fmt.Errorf("%s: differing 2xx response schemas need manual mapping", opID)
				}
			}
			description, _ := operation["description"].(string)
			if description == "" {
				description, _ = operation["summary"].(string)
			}
			if description == "" {
				description = opID
			}
			effect := effects[opID]
			if effect == "" {
				if method == "get" {
					effect = "read"
				} else {
					effect = "write"
				}
			}
			capability := map[string]any{"name": opID, "description": description, "method": strings.ToUpper(method), "path": path, "input_schema": input, "output_schema": first, "response_schemas": byStatus, "effect": effect}
			if wrapResponse {
				capability["response_mode"] = "wrap"
			}
			if emptySuccess {
				capability["allow_empty_success"] = true
			}
			found[opID] = capability
		}
	}
	caps := []any{}
	for _, op := range operations {
		capability, ok := found[op]
		if !ok {
			return nil, fmt.Errorf("operationIds not found: %s", op)
		}
		caps = append(caps, capability)
	}
	draft := map[string]any{"schema": "agenstra.rest-pack.v2", "name": name, "version": "1.0.0", "guidance": fmt.Sprintf("Use the reviewed %s REST capabilities for their declared purpose.", name), "base_url_env": baseURLEnv, "capabilities": caps}
	if tokenEnv != "" {
		draft["token_env"] = tokenEnv
	}
	return draft, nil
}
