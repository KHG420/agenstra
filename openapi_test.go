package agenstra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAPIExplicitBearerAndLocalRefs(t *testing.T) {
	doc := JSON{"openapi": "3.0.3", "components": JSON{"securitySchemes": JSON{"RecordsAuth": JSON{"type": "http", "scheme": "bearer"}}, "schemas": JSON{"Record": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}}}}, "security": []any{JSON{"RecordsAuth": []any{}}}, "paths": JSON{"/records/{id}": JSON{"get": JSON{"operationId": "getRecords", "parameters": []any{JSON{"name": "id", "in": "path", "required": true, "schema": JSON{"type": "string"}}}, "responses": JSON{"200": JSON{"content": JSON{"application/json": JSON{"schema": JSON{"$ref": "#/components/schemas/Record"}}}}}}}}}
	_, err := ImportOpenAPIDocument(doc, "records", "RECORDS_URL", []string{"getRecords"}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "--token-env") {
		t.Fatalf("bearer silently accepted: %v", err)
	}
	draft, err := ImportOpenAPIDocument(doc, "records", "RECORDS_URL", []string{"getRecords"}, nil, "RECORDS_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if draft["token_env"] != "RECORDS_TOKEN" {
		t.Fatal(draft)
	}
	caps := draft["capabilities"].([]any)
	cap := caps[0].(map[string]any)
	output := cap["output_schema"].(map[string]any)
	if output["$ref"] != "#/$defs/Record" {
		t.Fatalf("schema reference not local: %v", output)
	}
	if err := ValidatePackManifest(draft, nil); err != nil {
		t.Fatalf("draft invalid: %v", err)
	}
	doc["components"].(map[string]any)["securitySchemes"].(map[string]any)["RecordsAuth"] = JSON{"type": "oauth2", "flows": JSON{}}
	_, err = ImportOpenAPIDocument(doc, "records", "RECORDS_URL", []string{"getRecords"}, nil, "RECORDS_TOKEN")
	if err == nil || !strings.Contains(err.Error(), "unsupported security scheme") {
		t.Fatalf("oauth accepted: %v", err)
	}
}

func importTestSchema(t *testing.T, schema JSON, version string, components JSON) JSON {
	t.Helper()
	doc := JSON{
		"openapi":    version,
		"components": JSON{"schemas": components},
		"paths": JSON{"/calculate": JSON{"post": JSON{
			"operationId": "calculateValue",
			"requestBody": JSON{"required": true, "content": JSON{"application/json": JSON{"schema": schema}}},
			"responses":   JSON{"200": JSON{"content": JSON{"application/json": JSON{"schema": schema}}}},
		}}},
	}
	draft, err := ImportOpenAPIDocument(doc, "calculation", "CALCULATION_URL", []string{"calculateValue"}, map[string]string{"calculateValue": "compute"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return draft
}

func TestOpenAPI30ExclusiveBoundsValidateRequestAndResponse(t *testing.T) {
	draft := importTestSchema(t, JSON{"$ref": "#/components/schemas/Measurement"}, "3.0.3", JSON{
		"Measurement": JSON{"type": "object", "required": []any{"value"}, "properties": JSON{
			"value": JSON{"type": "number", "minimum": json.Number("0"), "exclusiveMinimum": true, "maximum": json.Number("10"), "exclusiveMaximum": true},
		}},
	})
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		value := 5
		if calls > 1 {
			value = 10
		}
		if err := json.NewEncoder(w).Encode(JSON{"value": value}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	pack, err := LoadRestPack(writeTestManifest(t, draft), map[string]string{"CALCULATION_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	for _, value := range []int{0, 10} {
		result, err := pack.Invoke(context.Background(), "calculateValue", JSON{"body": JSON{"value": value}}, nil)
		if err != nil || result.ErrorCode != "capability_input_invalid" || calls != 0 {
			t.Fatalf("boundary %d reached provider: %+v %v calls=%d", value, result, err, calls)
		}
	}
	result, err := pack.Invoke(context.Background(), "calculateValue", JSON{"body": JSON{"value": 5}}, nil)
	if err != nil || result.ErrorCode != "" || calls != 1 {
		t.Fatalf("valid measurement rejected: %+v %v", result, err)
	}
	result, err = pack.Invoke(context.Background(), "calculateValue", JSON{"body": JSON{"value": 5}}, nil)
	if err != nil || result.ErrorCode != "upstream_response_invalid" || calls != 2 {
		t.Fatalf("invalid response accepted: %+v %v", result, err)
	}
}

func TestOpenAPIBoundsPreserveVersionSemantics(t *testing.T) {
	for _, test := range []struct {
		version       string
		exclusive     any
		boundaryValid bool
	}{
		{"3.0.3", false, true},
		{"3.1.0", json.Number("1"), false},
	} {
		t.Run(test.version, func(t *testing.T) {
			draft := importTestSchema(t, JSON{"type": "object", "properties": JSON{
				"value": JSON{"type": "number", "minimum": 0, "exclusiveMinimum": test.exclusive},
			}}, test.version, JSON{})
			output := draft["capabilities"].([]any)[0].(map[string]any)["output_schema"].(map[string]any)
			validator, err := validateLocalSchema(output, true)
			if err != nil {
				t.Fatal(err)
			}
			if valid := validateSchema(validator, JSON{"value": 0}) == nil; valid != test.boundaryValid {
				t.Fatalf("boundary validation changed: %v", output)
			}
			if err := validateSchema(validator, JSON{"value": 2}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenAPISchemaConversionPreservesPropertyNamesAndLiteralJSON(t *testing.T) {
	literal := JSON{"$ref": "literal-provider-value", "nullable": true}
	draft := importTestSchema(t, JSON{
		"type": "object", "required": []any{"nullable", "$ref"},
		"properties": JSON{
			"nullable": JSON{"type": "boolean"},
			"$ref":     JSON{"type": "string"},
			"metadata": JSON{"type": "object", "enum": []any{literal}, "default": literal},
			"optional": JSON{"type": "string", "nullable": true},
		},
		"example": literal,
	}, "3.0.3", JSON{})
	output := draft["capabilities"].([]any)[0].(map[string]any)["output_schema"].(map[string]any)
	props := output["properties"].(map[string]any)
	metadata := props["metadata"].(map[string]any)
	if len(props) != 4 || !reflect.DeepEqual(metadata["enum"], []any{literal}) || !reflect.DeepEqual(metadata["default"], literal) || !reflect.DeepEqual(output["example"], literal) {
		t.Fatalf("literal schema data changed: %v", output)
	}
	if err := ValidatePackManifest(draft, nil); err != nil {
		t.Fatalf("imported pack cannot load: %v", err)
	}
	validator, err := validateLocalSchema(output, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []JSON{
		{"nullable": true, "$ref": "resource", "metadata": literal},
		{"nullable": false, "$ref": "resource", "optional": nil},
	} {
		if err := validateSchema(validator, value); err != nil {
			t.Fatalf("valid literal rejected: %v", err)
		}
	}
	if err := validateSchema(validator, JSON{"nullable": "wrong type", "$ref": "resource"}); err == nil {
		t.Fatal("property validation was removed")
	}
}

func TestOpenAPI30ExclusiveBoundRequiresNumericInclusiveBound(t *testing.T) {
	for _, bound := range []struct{ exclusive, inclusive string }{
		{"exclusiveMinimum", "minimum"}, {"exclusiveMaximum", "maximum"},
	} {
		_, err := convertOpenAPISchema(JSON{"openapi": "3.0.3"}, JSON{bound.exclusive: true})
		if err == nil || !strings.Contains(err.Error(), bound.exclusive) || !strings.Contains(err.Error(), bound.inclusive) {
			t.Fatalf("missing %s accepted: %v", bound.inclusive, err)
		}
	}
}

func TestOpenAPIImportsArrayScalarAndEmptyResponses(t *testing.T) {
	doc := JSON{"openapi": "3.1.0", "components": JSON{"schemas": JSON{"Ids": JSON{"type": "array", "items": JSON{"type": "string"}}}}, "paths": JSON{
		"/ids":    JSON{"get": JSON{"operationId": "listIds", "responses": JSON{"200": JSON{"content": JSON{"application/json": JSON{"schema": JSON{"$ref": "#/components/schemas/Ids"}}}}}}},
		"/count":  JSON{"get": JSON{"operationId": "getCount", "responses": JSON{"200": JSON{"content": JSON{"application/json": JSON{"schema": JSON{"type": "integer"}}}}}}},
		"/delete": JSON{"delete": JSON{"operationId": "deleteItem", "responses": JSON{"204": JSON{"description": "Deleted"}}}},
	}}
	draft, err := ImportOpenAPIDocument(doc, "sample", "API_URL", []string{"listIds", "getCount", "deleteItem"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePackManifest(draft, nil); err != nil {
		t.Fatalf("imported manifest invalid: %v", err)
	}
	caps := draft["capabilities"].([]any)
	for _, index := range []int{0, 1} {
		cap := caps[index].(map[string]any)
		if cap["response_mode"] != "wrap" {
			t.Fatalf("non-object response lacks wrap mode: %v", cap)
		}
		output := cap["output_schema"].(map[string]any)
		validator, err := validateLocalSchema(output, true)
		if err != nil {
			t.Fatal(err)
		}
		valid := any([]any{"A"})
		invalid := any(json.Number("1"))
		if index == 1 {
			valid, invalid = json.Number("1"), []any{"A"}
		}
		if validateSchema(validator, JSON{"result": valid}) != nil || validateSchema(validator, JSON{"result": invalid}) == nil {
			t.Fatalf("bad generated output schema: %v", output)
		}
	}
	if caps[2].(map[string]any)["allow_empty_success"] != true {
		t.Fatalf("204 was not enabled: %v", caps[2])
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ids":
			_, _ = w.Write([]byte(`["A","B"]`))
		case "/count":
			_, _ = w.Write([]byte(`2`))
		case "/delete":
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	pack, err := LoadRestPack(writeTestManifest(t, draft), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	for _, tc := range []struct {
		name string
		want JSON
	}{
		{"listIds", JSON{"result": []any{"A", "B"}}},
		{"getCount", JSON{"result": json.Number("2")}},
		{"deleteItem", JSON{}},
	} {
		result, err := pack.Invoke(context.Background(), tc.name, JSON{}, nil)
		if err != nil || result.ErrorCode != "" || !reflect.DeepEqual(result.Data, tc.want) {
			t.Fatalf("%s: result=%+v err=%v want=%v", tc.name, result, err, tc.want)
		}
	}
}

func TestOpenAPIMixedSuccessShapesRequireManualMapping(t *testing.T) {
	doc := JSON{"openapi": "3.1.0", "paths": JSON{"/records": JSON{"get": JSON{
		"operationId": "listRecords",
		"responses": JSON{
			"200": JSON{"content": JSON{"application/json": JSON{"schema": JSON{"type": "array", "items": JSON{"type": "string"}}}}},
			"204": JSON{"description": "No records"},
		},
	}}}}
	_, err := ImportOpenAPIDocument(doc, "records", "API_URL", []string{"listRecords"}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "listRecords: differing 2xx response schemas need manual mapping") {
		t.Fatalf("mixed 2xx responses silently accepted: %v", err)
	}
}
