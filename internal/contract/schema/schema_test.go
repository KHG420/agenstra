package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompileChecksOnlySchemaPositions(t *testing.T) {
	literal := map[string]any{"$ref": "provider-data", "$id": "provider-id", "$schema": "provider-metadata"}
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"$ref":     map[string]any{"type": "string"},
		"metadata": map[string]any{"enum": []any{literal}, "default": literal},
	}}
	if _, err := CompileLocal(schema, true); err != nil {
		t.Fatalf("literal map[string]any treated as a schema instruction: %v", err)
	}
	for _, nested := range []map[string]any{
		{"properties": map[string]any{"item": map[string]any{"$ref": "https://external.invalid/schema"}}},
		{"items": map[string]any{"$ref": "https://external.invalid/schema"}},
		{"allOf": []any{map[string]any{"$ref": "https://external.invalid/schema"}}},
		{"additionalProperties": map[string]any{"$id": "https://external.invalid/schema"}},
	} {
		if _, err := CompileLocal(nested, true); err == nil {
			t.Fatalf("unsafe nested schema accepted: %v", nested)
		}
	}
}

func TestValidateNormalizesNumbersWithoutCoercingOtherTypes(t *testing.T) {
	contract, err := CompileLocal(map[string]any{"const": 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{1, float64(1)} {
		if err := Validate(contract, value); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []any{"1", true, 2} {
		if err := Validate(contract, value); err == nil {
			t.Fatal("different JSON value accepted", value)
		}
	}
}

func TestCompileNormalizesTypedJSONDeclarations(t *testing.T) {
	input := map[string]any{"type": "object", "properties": map[string]any{
		"count": map[string]any{"type": "integer", "minimum": json.Number("9007199254740993")},
		"mode":  map[string]any{"enum": []string{"preview", "read"}},
	}, "required": []string{"count", "mode"}, "additionalProperties": false}
	contract, err := CompileLocal(input, false)
	if err != nil {
		t.Fatal("valid typed JSON declaration failed", err)
	}
	if err := Validate(contract, map[string]any{"count": int64(9007199254740993), "mode": "preview"}); err != nil {
		t.Fatal("normalization changed an exact integer constraint", err)
	}
	for _, value := range []map[string]any{
		{"count": int64(9007199254740992), "mode": "preview"},
		{"count": int64(9007199254740993), "mode": "unknown"},
		{"mode": "preview"},
	} {
		if Validate(contract, value) == nil {
			t.Fatal("normalization relaxed a declared constraint", value)
		}
	}
	if _, ok := input["required"].([]string); !ok {
		t.Fatal("normalization changed caller-owned schema")
	}
	for _, keyword := range []string{"anyOf", "allOf", "oneOf"} {
		_, err := CompileLocal(map[string]any{keyword: []map[string]any{{"$ref": "https://external.invalid/schema"}}}, false)
		if err == nil || !strings.Contains(err.Error(), "schema requires local references") {
			t.Fatal("typed branches bypassed the local-reference guard", keyword, err)
		}
	}
}
