// Package schema compiles local JSON contracts and validates finite JSON values.
package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// CompileLocal compiles a local-only JSON Schema, with optional REST dialect restrictions.
func CompileLocal(schema map[string]any, strictREST bool) (*jsonschema.Schema, error) {
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

// Validate checks normalized finite JSON against a compiled contract; nil permits any value.
func Validate(s *jsonschema.Schema, v any) error {
	if s == nil {
		return nil
	}
	raw, err := jsonvalue.Canonical(v)
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
