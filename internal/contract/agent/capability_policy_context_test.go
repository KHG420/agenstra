package agent

import (
	"bytes"
	"testing"
)

func TestCapabilityCatalogOmitsRuntimeVisibilityPolicy(t *testing.T) {
	for _, policy := range []*ModelOutput{nil, {Paths: [][]string{}}, {Paths: [][]string{{"result", "id"}}}} {
		capability := CapabilityDescription{Name: "records.read", Version: "1", Description: "Read a record",
			InputSchema:  JSON{"type": "object", "properties": JSON{"id": JSON{"type": "integer"}}},
			OutputSchema: JSON{"type": "object"}, OutputFields: JSON{"result": "Saved record"}, ModelOutput: policy}
		before, err := CanonicalJSON(capability)
		if err != nil {
			t.Fatal(err)
		}
		for _, full := range []bool{false, true} {
			view := capability.CatalogView(full)
			if _, exists := view["model_output"]; exists {
				t.Fatal("execution visibility policy was sent as model catalog data")
			}
			// Everything other than the already-deferred output schema and the
			// runtime's visibility policy keeps its existing model meaning.
			expected, err := ObjectOf(capability)
			if err != nil {
				t.Fatal(err)
			}
			delete(expected, "output_schema")
			delete(expected, "model_output")
			for key, value := range expected {
				if value == nil {
					delete(expected, key)
				}
			}
			actual, err := CanonicalJSON(view)
			want, wantErr := CanonicalJSON(expected)
			if err != nil || wantErr != nil || !bytes.Equal(actual, want) {
				t.Fatal("catalog policy omission changed other capability metadata", err, wantErr)
			}
		}
		after, err := CanonicalJSON(capability)
		if err != nil || !bytes.Equal(before, after) || capability.ModelOutput != policy {
			t.Fatal("model catalog mutated the provider's execution contract", err)
		}
	}
}
