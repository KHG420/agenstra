package agent

import (
	"encoding/json"
	"math"
	"testing"
)

func TestCapabilityCatalogPreservesExactNumericContracts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema JSON
	}{
		{name: "Go integer bounds", schema: JSON{"type": "object", "properties": JSON{
			"id": JSON{"type": "integer", "minimum": int64(math.MinInt64), "maximum": int64(math.MaxInt64)},
		}}},
		{name: "JSON integer and decimal constraints", schema: JSON{"type": "object", "properties": JSON{
			"id": JSON{"type": "integer", "enum": []any{json.Number("9007199254740993"), json.Number("9223372036854775807")}},
			"amount": JSON{"type": "number", "const": json.Number("0.12345678901234567890123456789"),
				"multipleOf": json.Number("0.00000000000000000000000000001")},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capability := CapabilityDescription{Name: "records.read", InputSchema: tc.schema}
			want, err := CanonicalJSON(tc.schema)
			if err != nil {
				t.Fatal(err)
			}
			for _, full := range []bool{false, true} {
				view := capability.CatalogView(full)
				got, err := CanonicalJSON(view["input_schema"])
				if err != nil || string(got) != string(want) {
					t.Fatalf("catalog changed numeric contract (full=%v): got %s; want %s; error %v", full, got, want, err)
				}
				delete(view["input_schema"].(map[string]any), "properties")
				after, err := CanonicalJSON(capability.InputSchema)
				if err != nil || string(after) != string(want) {
					t.Fatal("catalog view changed its provider's original schema", err)
				}
			}
		})
	}
}
