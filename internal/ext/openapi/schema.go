package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func openapiPointer(doc map[string]any, ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("external reference is unsupported: %s", ref)
	}
	var cur any = doc
	for _, part := range strings.Split(ref[2:], "/") {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return nil, fmt.Errorf("invalid OpenAPI reference: %w", err)
		}
		part = decoded
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
func resolveOpenAPI(doc map[string]any, v any, seen map[string]bool) (map[string]any, error) {
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
func convertOpenAPISchema(doc map[string]any, source map[string]any) (map[string]any, error) {
	definitions := map[string]any{}
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
				siblings := map[string]any{}
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
				result := map[string]any{"$ref": "#/$defs/" + name}
				if strings.HasPrefix(version, "3.0.") && siblings["nullable"] == true {
					return map[string]any{"anyOf": []any{result, map[string]any{"type": "null"}}}, nil
				}
				if len(siblings) > 0 {
					extra, e := convert(siblings)
					if e != nil {
						return nil, e
					}
					return map[string]any{"allOf": []any{result, extra}}, nil
				}
				return result, nil
			}
			result := map[string]any{}
			for k, y := range x {
				if k == "nullable" && strings.HasPrefix(version, "3.0.") {
					continue
				}
				// Property names and literal enum/default/example data are not schemas.
				switch k {
				case "properties", "patternProperties", "$defs", "dependentSchemas":
					if schemas, ok := y.(map[string]any); ok {
						converted := map[string]any{}
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
				return map[string]any{"anyOf": []any{result, map[string]any{"type": "null"}}}, nil
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
func jsonMedia(doc map[string]any, parent map[string]any, label string) (map[string]any, error) {
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

func openAPIOutputSchema(schema map[string]any) (map[string]any, bool) {
	defs, _ := schema["$defs"].(map[string]any)
	// Provider results must be objects. Preserve their shape only when the
	// contract requires an object; nullable and mixed results need the existing
	// result wrapper even when their type is expressed through a composition.
	var objectOnly func(map[string]any, int) bool
	objectOnly = func(root map[string]any, depth int) bool {
		if root == nil || depth >= 16 {
			return false
		}
		if root["type"] == "object" {
			return true
		}
		if types, ok := root["type"].([]any); ok && len(types) > 0 {
			onlyObjects := true
			for _, kind := range types {
				onlyObjects = onlyObjects && kind == "object"
			}
			if onlyObjects {
				return true
			}
		}
		if ref, ok := root["$ref"].(string); ok && strings.HasPrefix(ref, "#/$defs/") {
			target, _ := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			if objectOnly(target, depth+1) {
				return true
			}
		}
		if branches, ok := root["allOf"].([]any); ok {
			for _, branch := range branches {
				child, _ := branch.(map[string]any)
				if objectOnly(child, depth+1) {
					return true
				}
			}
		}
		for _, keyword := range []string{"anyOf", "oneOf"} {
			if branches, ok := root[keyword].([]any); ok && len(branches) > 0 {
				onlyObjects := true
				for _, branch := range branches {
					child, _ := branch.(map[string]any)
					onlyObjects = onlyObjects && objectOnly(child, depth+1)
				}
				if onlyObjects {
					return true
				}
			}
		}
		return false
	}
	if objectOnly(schema, 0) {
		return schema, false
	}
	inner := map[string]any{}
	for key, value := range schema {
		if key != "$defs" {
			inner[key] = value
		}
	}
	wrapped := map[string]any{"type": "object", "properties": map[string]any{"result": inner}, "required": []any{"result"}, "additionalProperties": false}
	if definitions, ok := schema["$defs"]; ok {
		wrapped["$defs"] = definitions
	}
	return wrapped, true
}
func resolveParameters(doc map[string]any, v any) ([]map[string]any, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New("OpenAPI parameters must be an array")
	}
	out := []map[string]any{}
	for _, item := range items {
		p, e := resolveOpenAPI(doc, item, map[string]bool{})
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, nil
}
