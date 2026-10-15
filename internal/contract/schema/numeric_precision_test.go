package schema

import (
	"encoding/json"
	"testing"
)

func TestValidateKeepsExactJSONNumberConstraints(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contract map[string]any
		value    any
		valid    bool
	}{
		{"fraction is not an integer", map[string]any{"type": "integer"}, json.Number("2.00000000000000000001"), false},
		{"integral decimal at int64 maximum", map[string]any{"type": "integer", "maximum": json.Number("9223372036854775807")}, json.Number("9223372036854775807.0"), true},
		{"fraction below minimum", map[string]any{"type": "number", "minimum": 1}, json.Number("0.99999999999999999999"), false},
		{"tiny nonzero is not zero", map[string]any{"const": 0}, json.Number("1e-1000"), false},
		{"exact schema minimum", map[string]any{"type": "number", "minimum": json.Number("1.00000000000000000001")}, json.Number("1.0"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compiled, err := CompileLocal(tc.contract, false)
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(compiled, tc.value)
			if (err == nil) != tc.valid {
				t.Fatalf("exact JSON numeric constraint changed: valid=%v want=%v err=%v", err == nil, tc.valid, err)
			}
		})
	}
}

func TestValidateUsesTheStandardGoJSONShape(t *testing.T) {
	compiled, err := CompileLocal(map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(compiled, []int64{}); err != nil {
		t.Fatal("empty JSON array rejected", err)
	}
	if err := Validate(compiled, []int64{2}); err != nil {
		t.Fatal("typed JSON integer array rejected", err)
	}
	if err := Validate(compiled, []int64(nil)); err == nil {
		t.Fatal("a nil Go slice serializes as null, not an empty JSON array")
	}
}

func TestSchemaRejectsUnsupportedNumericExponents(t *testing.T) {
	compiled, err := CompileLocal(map[string]any{"type": "number", "minimum": 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range []string{"1e-1000001", "1e309", "1e1000001"} {
		value := json.Number(literal)
		if err := Validate(compiled, value); err == nil {
			t.Fatal("unsupported exponent reached the numeric validator", literal)
		}
		if _, err := CompileLocal(map[string]any{"minimum": value}, false); err == nil {
			t.Fatal("unsupported exponent reached schema compilation", literal)
		}
	}
	if err := Validate(compiled, json.Number("1e-1000")); err != nil {
		t.Fatal("supported tiny nonzero value rejected", err)
	}
}
