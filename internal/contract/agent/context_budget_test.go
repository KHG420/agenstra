package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestDeferredUnionSchemaKeepsTopLevelWrapperFields(t *testing.T) {
	cap := CapabilityDescription{Name: "records.write", InputSchema: JSON{"type": "object", "anyOf": []any{
		JSON{"type": "object", "properties": JSON{"operation": JSON{"const": "create"}, "arguments": JSON{"type": "object", "description": strings.Repeat("x", 2100), "properties": JSON{"request": JSON{"type": "object"}}}}},
		JSON{"allOf": []any{JSON{"properties": JSON{"operation": JSON{"const": "update"}, "arguments": JSON{"type": "object"}}}}},
	}}}
	view := cap.ModelView()
	if view["schema_requires_inspection"] != true || !reflect.DeepEqual(view["input_fields"], []string{"arguments", "operation"}) {
		t.Fatal("union schema lost its wrapper fields", view)
	}
}
