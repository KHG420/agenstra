package jsonvalue

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestCanonicalJSONPythonFloatsAndSeparators(t *testing.T) {
	value := map[string]any{"b": []any{1.0, json.Number("1e6"), json.Number("1e-6"), math.Copysign(0, -1)}, "a": "literal \\u2028 and actual \u2028"}
	raw, err := Canonical(value)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("invalid JSON: %s", raw)
	}
	expected := `{"a":"literal \\u2028 and actual ` + "\u2028" + `","b":[1.0,1000000.0,1e-06,-0.0]}`
	if string(raw) != expected {
		t.Fatalf("canonical mismatch:\n got %s\nwant %s", raw, expected)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded["a"].(string), `\u2028`) {
		t.Fatalf("literal escape corrupted: %v", decoded)
	}
}
