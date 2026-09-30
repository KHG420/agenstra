package agenstra

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

func ImportOpenAPI(path, name, baseURLEnv string, operations []string, effects map[string]string, tokenEnv string) (JSON, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc JSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return ImportOpenAPIDocument(doc, name, baseURLEnv, operations, effects, tokenEnv)
}
func openapiPointer(doc JSON, ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("external reference is unsupported: %s", ref)
	}
	var cur any = doc
	for _, part := range strings.Split(ref[2:], "/") {
		part, _ = url.PathUnescape(part)
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unresolved OpenAPI reference: %s", ref)
		}
		next, ok := obj[part]
		if !ok {
			return nil, fmt.Errorf("unresolved OpenAPI reference: %s", ref)
		}
		cur = next
	}
	return cur, nil
}
func resolveOpenAPI(doc JSON, v any, seen map[string]bool) (JSON, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("OpenAPI object must be a JSON object")
	}
	ref, has := obj["$ref"]
	if !has {
		return obj, nil
	}
	if len(obj) != 1 {
		return nil, errors.New("OpenAPI reference siblings are unsupported")
	}
	s, ok := ref.(string)
	if !ok {
		return nil, errors.New("OpenAPI reference must be a string")
	}
	if seen[s] {
		return nil, fmt.Errorf("cyclic OpenAPI reference: %s", s)
	}
	next, e := openapiPointer(doc, s)
	if e != nil {
		return nil, e
	}
	seen[s] = true
	defer delete(seen, s)
	return resolveOpenAPI(doc, next, seen)
}
func convertOpenAPISchema(doc JSON, source JSON) (JSON, error) {
	definitions := JSON{}
	active := map[string]bool{}
	version, _ := doc["openapi"].(string)
	var convert func(any) (any, error)
	convert = func(v any) (any, error) {
		switch x := v.(type) {
		case []any:
			out := []any{}
			for _, item := range x {
				c, e := convert(item)
				if e != nil {
					return nil, e
				}
				out = append(out, c)
			}
			return out, nil
		case map[string]any:
			if ref, ok := x["$ref"]; ok {
				r, ok := ref.(string)
				if !ok || !strings.HasPrefix(r, "#/components/schemas/") {
					return nil, fmt.Errorf("unsupported schema reference: %v", ref)
				}
				name := strings.TrimPrefix(r, "#/components/schemas/")
				name = strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
				if name == "" || strings.ContainsAny(name, "/~") {
					return nil, fmt.Errorf("unsupported schema name in reference: %s", r)
				}
				if _, done := definitions[name]; !done && !active[name] {
					active[name] = true
					target, e := openapiPointer(doc, r)
					if e != nil {
						return nil, e
					}
					object, ok := target.(map[string]any)
					if !ok {
						return nil, errors.New("schema reference is not an object")
					}
					converted, e := convert(object)
					if e != nil {
						return nil, e
					}
					definitions[name] = converted
					delete(active, name)
				}
				siblings := JSON{}
				for k, y := range x {
					if k != "$ref" {
						siblings[k] = y
					}
				}
				if len(siblings) > 0 && !strings.HasPrefix(version, "3.1.") {
					if len(siblings) != 1 || siblings["nullable"] != true {
						return nil, errors.New("OpenAPI 3.0 schema reference siblings are unsupported")
					}
				}
				result := JSON{"$ref": "#/$defs/" + name}
				if strings.HasPrefix(version, "3.0.") && siblings["nullable"] == true {
					return JSON{"anyOf": []any{result, JSON{"type": "null"}}}, nil
				}
				if len(siblings) > 0 {
					extra, e := convert(siblings)
					if e != nil {
						return nil, e
					}
					return JSON{"allOf": []any{result, extra}}, nil
				}
				return result, nil
			}
			result := JSON{}
			for k, y := range x {
				if k == "nullable" && strings.HasPrefix(version, "3.0.") {
					continue
				}
				// Property names and literal enum/default/example data are not schemas.
				switch k {
				case "properties", "patternProperties", "$defs", "dependentSchemas":
					if schemas, ok := y.(map[string]any); ok {
						converted := JSON{}
						for name, schema := range schemas {
							c, e := convert(schema)
							if e != nil {
								return nil, e
							}
							converted[name] = c
						}
						result[k] = converted
						continue
					}
				case "allOf", "anyOf", "oneOf", "prefixItems", "items", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "propertyNames", "contains", "not", "if", "then", "else", "contentSchema":
					c, e := convert(y)
					if e != nil {
						return nil, e
					}
					result[k] = c
					continue
				}
				result[k] = y
			}
			if strings.HasPrefix(version, "3.0.") {
				for _, bound := range []struct{ exclusive, inclusive string }{
					{"exclusiveMinimum", "minimum"}, {"exclusiveMaximum", "maximum"},
				} {
					if enabled, ok := result[bound.exclusive].(bool); ok {
						delete(result, bound.exclusive)
						if enabled {
							switch result[bound.inclusive].(type) {
							case json.Number, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
								result[bound.exclusive] = result[bound.inclusive]
								delete(result, bound.inclusive)
							default:
								return nil, fmt.Errorf("%s=true requires numeric %s", bound.exclusive, bound.inclusive)
							}
						}
					}
				}
			}
			if strings.HasPrefix(version, "3.0.") && x["nullable"] == true {
				return JSON{"anyOf": []any{result, JSON{"type": "null"}}}, nil
			}
			return result, nil
		default:
			return v, nil
		}
	}
	result, e := convert(source)
	if e != nil {
		return nil, e
	}
	schema := result.(map[string]any)
	if len(definitions) > 0 {
		schema["$defs"] = definitions
	}
	return schema, nil
}
func jsonMedia(doc JSON, parent JSON, label string) (JSON, error) {
	content, ok := parent["content"].(map[string]any)
	if !ok || len(content) != 1 {
		return nil, fmt.Errorf("%s: only application/json content is supported", label)
	}
	media, ok := content["application/json"]
	if !ok {
		return nil, fmt.Errorf("%s: only application/json content is supported", label)
	}
	resolved, e := resolveOpenAPI(doc, media, map[string]bool{})
	if e != nil {
		return nil, e
	}
	schema, ok := resolved["schema"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: JSON content requires a schema", label)
	}
	return schema, nil
}
func resolveParameters(doc JSON, v any) ([]JSON, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New("OpenAPI parameters must be an array")
	}
	out := []JSON{}
	for _, item := range items {
		p, e := resolveOpenAPI(doc, item, map[string]bool{})
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, nil
}
func ImportOpenAPIDocument(doc JSON, name, baseURLEnv string, operations []string, effects map[string]string, tokenEnv string) (JSON, error) {
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
	found := map[string]JSON{}
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
			parameterMap := map[string]JSON{}
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
			byLocation := map[string]JSON{"path": {}, "query": {}}
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
			props := JSON{}
			rootRequired := []any{}
			for _, loc := range []string{"path", "query"} {
				if len(byLocation[loc]) > 0 {
					props[loc] = JSON{"type": "object", "properties": byLocation[loc], "required": required[loc], "additionalProperties": false}
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
			input, e := convertOpenAPISchema(doc, JSON{"type": "object", "properties": props})
			if e != nil {
				return nil, e
			}
			input["required"] = rootRequired
			input["additionalProperties"] = false
			responses, ok := operation["responses"].(map[string]any)
			if !ok {
				return nil, errors.New("responses must be an object")
			}
			byStatus := JSON{}
			for status, source := range responses {
				if regexp.MustCompile(`^2[0-9]{2}$`).MatchString(status) {
					resolved, e := resolveOpenAPI(doc, source, map[string]bool{})
					if e != nil {
						return nil, e
					}
					schema, e := jsonMedia(doc, resolved, opID+" response "+status)
					if e != nil {
						return nil, e
					}
					converted, e := convertOpenAPISchema(doc, schema)
					if e != nil {
						return nil, e
					}
					byStatus[status] = converted
				}
			}
			if len(byStatus) == 0 {
				return nil, errors.New("a JSON 2xx response schema is required")
			}
			var first JSON
			for _, v := range byStatus {
				first = v.(map[string]any)
				break
			}
			firstRaw, _ := CanonicalJSON(first)
			for _, v := range byStatus {
				raw, _ := CanonicalJSON(v)
				if !bytes.Equal(raw, firstRaw) {
					return nil, errors.New("differing 2xx response schemas need manual mapping")
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
			found[opID] = JSON{"name": opID, "description": description, "method": strings.ToUpper(method), "path": path, "input_schema": input, "output_schema": first, "response_schemas": byStatus, "effect": effect}
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
	draft := JSON{"schema": "agenstra.rest-pack.v2", "name": name, "version": "1.0.0", "guidance": fmt.Sprintf("Use the reviewed %s REST capabilities for their declared purpose.", name), "base_url_env": baseURLEnv, "capabilities": caps}
	if tokenEnv != "" {
		draft["token_env"] = tokenEnv
	}
	return draft, nil
}
