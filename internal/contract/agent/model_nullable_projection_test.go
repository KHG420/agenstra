package agent

import (
	"testing"
)

func nullableProjectionSchema(keyword string) JSON {
	price := JSON{"type": "object", "properties": JSON{"input_price": JSON{"type": []any{"number", "null"}}, "secret": JSON{"type": "string"}}}
	pricing := JSON{keyword: []any{JSON{"type": "array", "items": price}, JSON{"type": "null"}}}
	return JSON{"type": "object", "properties": JSON{
		"result":   JSON{"type": "array", "items": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "integer"}, "model_pricing": pricing}}},
		"optional": JSON{keyword: []any{JSON{"type": "object", "properties": JSON{"id": JSON{"type": "integer"}}}, JSON{"type": "null"}}},
	}}
}

func TestNullableModelOutputAllowsDeclaredContainerBranches(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		schema := nullableProjectionSchema(keyword)
		projection := &ModelOutput{Paths: [][]string{{"result", "*", "id"}, {"result", "*", "model_pricing", "*", "input_price"}, {"optional", "id"}}}
		if err := ValidateModelOutputSchema(projection, schema); err != nil {
			t.Fatalf("%s nullable container rejected: %v", keyword, err)
		}
	}
	schema := JSON{"type": "object", "properties": JSON{"items": JSON{"type": []any{"array", "null"}, "items": JSON{"type": []any{"object", "null"}, "properties": JSON{"id": JSON{"type": "integer"}}}}}}
	if err := ValidateModelOutputSchema(&ModelOutput{Paths: [][]string{{"items", "*", "id"}}}, schema); err != nil {
		t.Fatalf("nullable type array rejected: %v", err)
	}
}

func TestNullableModelOutputPreservesNullEmptyMissingAndPrivateFields(t *testing.T) {
	projection := &ModelOutput{Paths: [][]string{{"result", "*", "id"}, {"result", "*", "model_pricing", "*", "input_price"}, {"optional", "id"}}}
	data := JSON{"result": []any{
		JSON{"id": 1, "model_pricing": nil, "secret": "hidden-one"},
		JSON{"id": 2, "model_pricing": []any{}},
		JSON{"id": 3},
		JSON{"id": 4, "model_pricing": []any{JSON{"input_price": 0, "secret": "hidden-price"}, JSON{"input_price": nil}}},
	}, "optional": nil}
	actual, err := CanonicalJSON(projectedModelData(data, projection))
	want := `{"optional":null,"result":[{"id":1,"model_pricing":null},{"id":2,"model_pricing":[]},{"id":3},{"id":4,"model_pricing":[{"input_price":0},{"input_price":null}]}]}`
	if err != nil || string(actual) != want {
		t.Fatalf("nullable projection: %s want %s error %v", actual, want, err)
	}
	for _, test := range []struct {
		data JSON
		want string
	}{
		{JSON{"optional": nil}, `{"optional":null}`},
		{JSON{}, `{}`},
		{JSON{"optional": JSON{"id": 0, "secret": "hidden"}}, `{"optional":{"id":0}}`},
	} {
		actual, err := CanonicalJSON(projectedModelData(test.data, &ModelOutput{Paths: [][]string{{"optional", "id"}}}))
		if err != nil || string(actual) != test.want {
			t.Fatalf("nullable object: %s want %s error %v", actual, test.want, err)
		}
	}
}

func TestNullableModelOutputRequiresPathInEveryNonNullBranch(t *testing.T) {
	field := JSON{"type": "integer"}
	first := JSON{"type": "object", "properties": JSON{"id": field}}
	second := JSON{"type": "object", "properties": JSON{"other": field}}
	for _, variants := range [][]any{
		{first, second, JSON{"type": "null"}},
		{first, JSON{"type": "string"}, JSON{"type": "null"}},
		{JSON{"type": "null"}},
	} {
		schema := JSON{"type": "object", "properties": JSON{"item": JSON{"anyOf": variants}}}
		if err := ValidateModelOutputSchema(&ModelOutput{Paths: [][]string{{"item", "id"}}}, schema); err == nil {
			t.Fatalf("undeclared or ambiguous path accepted: %+v", variants)
		}
	}
	// A field shared by both declared non-null object branches remains valid.
	second["properties"].(map[string]any)["id"] = field
	schema := JSON{"type": "object", "properties": JSON{"item": JSON{"anyOf": []any{first, second, JSON{"type": "null"}}}}}
	if err := ValidateModelOutputSchema(&ModelOutput{Paths: [][]string{{"item", "id"}}}, schema); err != nil {
		t.Fatalf("shared branch path rejected: %v", err)
	}
}
