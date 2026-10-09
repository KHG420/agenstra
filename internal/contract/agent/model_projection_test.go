package agent

import (
	"strings"
	"testing"
)

func TestModelOutputRejectsInvalidPaths(t *testing.T) {
	for _, output := range []*ModelOutput{
		{Paths: nil},
		{Paths: [][]string{{}}},
		{Paths: [][]string{{"items", "0", "secret"}, {"items", "0", "secret"}}},
		{Paths: [][]string{{""}}},
		{Paths: [][]string{{strings.Repeat("x", 129)}}},
	} {
		if err := ValidateModelOutput(output); err == nil {
			t.Fatalf("accepted invalid projection: %+v", output)
		}
	}
	if err := ValidateModelOutput(&ModelOutput{Paths: [][]string{}}); err != nil {
		t.Fatalf("explicit empty projection rejected: %v", err)
	}
	schema := JSON{"type": "object", "properties": JSON{"items": JSON{"type": "array", "items": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}}}}}
	if err := ValidateModelOutputSchema(&ModelOutput{Paths: [][]string{{"items", "0", "id"}}}, schema); err == nil {
		t.Fatal("accepted a partial array index projection")
	}

	visible := projectedModelData(JSON{"items": []any{JSON{"id": "A", "secret": "hidden"}}}, &ModelOutput{Paths: [][]string{{"items", "0", "id"}}})
	if len(visible) != 0 {
		t.Fatalf("array index was interpreted as object field: %v", visible)
	}
}
