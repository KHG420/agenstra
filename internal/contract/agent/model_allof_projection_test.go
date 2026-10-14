package agent

import "testing"

func singleAllOfProjectionSchema() JSON {
	connection := JSON{"allOf": []any{JSON{"$ref": "#/$defs/Connection"}}, "description": "Provider connection metadata"}
	return JSON{"$defs": JSON{
		"Connection": JSON{"type": "object", "properties": JSON{
			"endpoint": JSON{"type": "string"}, "secret": JSON{"type": "string"},
		}},
	}, "type": "object", "properties": JSON{
		"connection": connection,
		"items": JSON{"type": "array", "items": JSON{"allOf": []any{
			JSON{"type": "object", "properties": JSON{"connection": connection, "count": JSON{"type": "integer"}}},
		}}},
	}}
}

func TestSingleAllOfModelOutputTraversesDeclaredFields(t *testing.T) {
	base := singleAllOfProjectionSchema()
	for _, schema := range []JSON{base, {"$defs": base["$defs"], "allOf": []any{base}}} {
		before, err := CanonicalJSON(schema)
		if err != nil {
			t.Fatal(err)
		}
		projection := &ModelOutput{Paths: [][]string{{"connection", "endpoint"}, {"items", "*", "connection", "endpoint"}, {"items", "*", "count"}}}
		if err := ValidateModelOutputSchema(projection, schema); err != nil {
			t.Fatalf("single allOf annotation wrapper rejected: %v", err)
		}
		fact := Fact{Value: JSON{"data": JSON{
			"connection": JSON{"endpoint": "https://api.example.invalid", "secret": "private-one"},
			"items":      []any{JSON{"connection": JSON{"endpoint": "https://row.example.invalid", "secret": "private-two"}, "count": 0}},
		}}, ModelOutput: projection}
		visible, err := CanonicalJSON(ModelFactValue(fact))
		want := `{"data":{"connection":{"endpoint":"https://api.example.invalid"},"items":[{"connection":{"endpoint":"https://row.example.invalid"},"count":0}]}}`
		if err != nil || string(visible) != want {
			t.Fatalf("projected fields or zero changed: %s error %v", visible, err)
		}
		if fact.Value["data"].(map[string]any)["connection"].(map[string]any)["secret"] != "private-one" {
			t.Fatal("complete private evidence was modified")
		}
		after, err := CanonicalJSON(schema)
		if err != nil || string(before) != string(after) {
			t.Fatal("output schema was modified")
		}
	}
}

func TestSingleAllOfModelOutputRejectsUndeclaredFields(t *testing.T) {
	for _, path := range [][]string{{"connection", "missing"}, {"items", "*", "connection", "missing"}, {"items", "0", "count"}} {
		if err := ValidateModelOutputSchema(&ModelOutput{Paths: [][]string{path}}, singleAllOfProjectionSchema()); err == nil {
			t.Fatalf("undeclared path accepted: %v", path)
		}
	}
}

func TestSingleAllOfModelOutputRetainsNestingBound(t *testing.T) {
	schema := JSON{"$defs": JSON{"Loop": JSON{"allOf": []any{JSON{"$ref": "#/$defs/Loop"}}}}, "$ref": "#/$defs/Loop"}
	if err := ValidateModelOutputSchema(&ModelOutput{Paths: [][]string{{"endpoint"}}}, schema); err == nil {
		t.Fatal("recursive alias accepted")
	}
}
