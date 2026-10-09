package schema

import "testing"

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
