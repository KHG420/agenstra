package agent

import (
	"strings"
	"testing"
)

func arrayProjectionSchema() JSON {
	return JSON{"type": "object", "properties": JSON{
		"items": JSON{"type": "array", "items": JSON{"type": "object", "properties": JSON{
			"id": JSON{"type": "integer"}, "balance": JSON{"type": "number"},
			"enabled": JSON{"type": "boolean"}, "label": JSON{"type": []any{"string", "null"}},
			"secret": JSON{"type": "string"}, "children": JSON{"type": "array", "items": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "integer"}}}},
		}}},
		"*": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "integer"}}},
	}}
}

func TestArrayModelOutputProjectionPreservesRowsAndPrivateEvidence(t *testing.T) {
	projection := &ModelOutput{Paths: [][]string{{"items", "*", "id"}, {"items", "*", "balance"}, {"items", "*", "enabled"}, {"items", "*", "label"}}}
	if err := ValidateModelOutputSchema(projection, arrayProjectionSchema()); err != nil {
		t.Fatalf("declared array field projection rejected: %v", err)
	}
	full := JSON{"items": []any{JSON{"id": 1, "balance": 0, "enabled": false, "label": nil, "secret": "private-one"}, JSON{"secret": "private-two"}, JSON{"id": 3, "label": "third"}}}
	visible := projectedModelData(full, projection)
	got, err := CanonicalJSON(visible)
	if err != nil || string(got) != `{"items":[{"balance":0,"enabled":false,"id":1,"label":null},{},{"id":3,"label":"third"}]}` {
		t.Fatalf("array structure or business values changed: %s %v", got, err)
	}
	if full["items"].([]any)[0].(map[string]any)["secret"] != "private-one" {
		t.Fatal("private evidence changed")
	}
	visible["items"].([]any)[0].(map[string]any)["id"] = 99
	if full["items"].([]any)[0].(map[string]any)["id"] != 1 {
		t.Fatal("visible array aliases complete evidence")
	}
}

func TestArrayModelOutputProjectionNestedEmptyAndTypedArrays(t *testing.T) {
	projection := &ModelOutput{Paths: [][]string{{"items", "*", "id"}, {"items", "*", "children", "*", "id"}}}
	if err := ValidateModelOutputSchema(projection, arrayProjectionSchema()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		data JSON
		want string
	}{
		{JSON{"items": []any{}}, `{"items":[]}`},
		{JSON{"items": []JSON{{"id": 4, "secret": "hidden", "children": []JSON{{"id": 5, "secret": "child-hidden"}}}, {"children": []any{}}}}, `{"items":[{"children":[{"id":5}],"id":4},{"children":[]}]}`},
		{JSON{}, `{}`},
	} {
		got, err := CanonicalJSON(projectedModelData(test.data, projection))
		if err != nil || string(got) != test.want {
			t.Fatalf("projection: %s want %s error %v", got, test.want, err)
		}
	}
}

func TestArrayModelOutputProjectionKeepsLiteralStarsAndRejectsIndexes(t *testing.T) {
	schema := arrayProjectionSchema()
	if err := ValidateModelOutputSchema(&ModelOutput{Paths: [][]string{{"*", "id"}}}, schema); err != nil {
		t.Fatalf("literal object star changed: %v", err)
	}
	got, err := CanonicalJSON(projectedModelData(JSON{"*": JSON{"id": 7, "secret": "hidden"}}, &ModelOutput{Paths: [][]string{{"*", "id"}}}))
	if err != nil || string(got) != `{"*":{"id":7}}` {
		t.Fatalf("literal object star: %s %v", got, err)
	}
	for _, path := range [][]string{{"items", "0", "id"}, {"items", "*", "undeclared"}, {"items", "*", "id", "*"}} {
		if err := ValidateModelOutputSchema(&ModelOutput{Paths: [][]string{path}}, schema); err == nil {
			t.Fatalf("invalid array projection accepted: %v", path)
		}
	}
	visible := projectedModelData(JSON{"items": []any{JSON{"id": 1, "secret": "hidden"}}}, &ModelOutput{Paths: [][]string{{"items", "0", "id"}}})
	if len(visible) != 0 {
		t.Fatalf("numeric index unexpectedly exposed data: %v", visible)
	}
}

func TestArrayModelOutputProjectionOverlappingPathsDoNotAlterPermissions(t *testing.T) {
	data := JSON{"items": []any{JSON{"id": 1, "secret": "permitted-by-parent"}}}
	for _, paths := range [][][]string{
		{{"items"}, {"items", "*", "id"}},
		{{"items", "*", "id"}, {"items"}},
	} {
		got, err := CanonicalJSON(projectedModelData(data, &ModelOutput{Paths: paths}))
		if err != nil || !strings.Contains(string(got), "permitted-by-parent") {
			t.Fatalf("explicit parent permission changed: %s %v", got, err)
		}
	}
}
