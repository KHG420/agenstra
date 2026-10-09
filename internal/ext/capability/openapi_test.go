package capability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestOpenAPIExplicitBearerAndLocalRefs(t *testing.T) {
	doc := agentcontract.JSON{"openapi": "3.0.3", "components": agentcontract.JSON{"securitySchemes": agentcontract.JSON{"RecordsAuth": agentcontract.JSON{"type": "http", "scheme": "bearer"}}, "schemas": agentcontract.JSON{"Record": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}}}}, "security": []any{agentcontract.JSON{"RecordsAuth": []any{}}}, "paths": agentcontract.JSON{"/records/{id}": agentcontract.JSON{"get": agentcontract.JSON{"operationId": "getRecords", "parameters": []any{agentcontract.JSON{"name": "id", "in": "path", "required": true, "schema": agentcontract.JSON{"type": "string"}}}, "responses": agentcontract.JSON{"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": agentcontract.JSON{"$ref": "#/components/schemas/Record"}}}}}}}}}
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
	doc["components"].(map[string]any)["securitySchemes"].(map[string]any)["RecordsAuth"] = agentcontract.JSON{"type": "oauth2", "flows": agentcontract.JSON{}}
	_, err = ImportOpenAPIDocument(doc, "records", "RECORDS_URL", []string{"getRecords"}, nil, "RECORDS_TOKEN")
	if err == nil || !strings.Contains(err.Error(), "unsupported security scheme") {
		t.Fatalf("oauth accepted: %v", err)
	}
}

func importTestSchema(t *testing.T, schema agentcontract.JSON, version string, components agentcontract.JSON) agentcontract.JSON {
	t.Helper()
	doc := agentcontract.JSON{
		"openapi":    version,
		"components": agentcontract.JSON{"schemas": components},
		"paths": agentcontract.JSON{"/calculate": agentcontract.JSON{"post": agentcontract.JSON{
			"operationId": "calculateValue",
			"requestBody": agentcontract.JSON{"required": true, "content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": schema}}},
			"responses":   agentcontract.JSON{"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": schema}}}},
		}}},
	}
	draft, err := ImportOpenAPIDocument(doc, "calculation", "CALCULATION_URL", []string{"calculateValue"}, map[string]string{"calculateValue": "compute"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return draft
}

func TestOpenAPI30ExclusiveBoundsValidateRequestAndResponse(t *testing.T) {
	draft := importTestSchema(t, agentcontract.JSON{"$ref": "#/components/schemas/Measurement"}, "3.0.3", agentcontract.JSON{
		"Measurement": agentcontract.JSON{"type": "object", "required": []any{"value"}, "properties": agentcontract.JSON{
			"value": agentcontract.JSON{"type": "number", "minimum": json.Number("0"), "exclusiveMinimum": true, "maximum": json.Number("10"), "exclusiveMaximum": true},
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
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"value": value}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	pack, err := LoadRestPack(writeTestManifest(t, draft), map[string]string{"CALCULATION_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(pack.Close)
	for _, value := range []int{0, 10} {
		result, err := pack.Invoke(context.Background(), "calculateValue", agentcontract.JSON{"body": agentcontract.JSON{"value": value}}, nil)
		if err != nil || result.ErrorCode != "capability_input_invalid" || calls != 0 {
			t.Fatalf("boundary %d reached provider: %+v %v calls=%d", value, result, err, calls)
		}
	}
	result, err := pack.Invoke(context.Background(), "calculateValue", agentcontract.JSON{"body": agentcontract.JSON{"value": 5}}, nil)
	if err != nil || result.ErrorCode != "" || calls != 1 {
		t.Fatalf("valid measurement rejected: %+v %v", result, err)
	}
	result, err = pack.Invoke(context.Background(), "calculateValue", agentcontract.JSON{"body": agentcontract.JSON{"value": 5}}, nil)
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
			draft := importTestSchema(t, agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
				"value": agentcontract.JSON{"type": "number", "minimum": 0, "exclusiveMinimum": test.exclusive},
			}}, test.version, agentcontract.JSON{})
			output := draft["capabilities"].([]any)[0].(map[string]any)["output_schema"].(map[string]any)
			validator, err := agentcontract.ValidateLocalSchema(output, true)
			if err != nil {
				t.Fatal(err)
			}
			if valid := agentcontract.ValidateSchema(validator, agentcontract.JSON{"value": 0}) == nil; valid != test.boundaryValid {
				t.Fatalf("boundary validation changed: %v", output)
			}
			if err := agentcontract.ValidateSchema(validator, agentcontract.JSON{"value": 2}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenAPISchemaConversionPreservesPropertyNamesAndLiteralJSON(t *testing.T) {
	literal := agentcontract.JSON{"$ref": "literal-provider-value", "nullable": true}
	draft := importTestSchema(t, agentcontract.JSON{
		"type": "object", "required": []any{"nullable", "$ref"},
		"properties": agentcontract.JSON{
			"nullable": agentcontract.JSON{"type": "boolean"},
			"$ref":     agentcontract.JSON{"type": "string"},
			"metadata": agentcontract.JSON{"type": "object", "enum": []any{literal}, "default": literal},
			"optional": agentcontract.JSON{"type": "string", "nullable": true},
		},
		"example": literal,
	}, "3.0.3", agentcontract.JSON{})
	output := draft["capabilities"].([]any)[0].(map[string]any)["output_schema"].(map[string]any)
	props := output["properties"].(map[string]any)
	metadata := props["metadata"].(map[string]any)
	if len(props) != 4 || !reflect.DeepEqual(metadata["enum"], []any{literal}) || !reflect.DeepEqual(metadata["default"], literal) || !reflect.DeepEqual(output["example"], literal) {
		t.Fatalf("literal schema data changed: %v", output)
	}
	if err := ValidatePackManifest(draft, nil); err != nil {
		t.Fatalf("imported pack cannot load: %v", err)
	}
	validator, err := agentcontract.ValidateLocalSchema(output, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []agentcontract.JSON{
		{"nullable": true, "$ref": "resource", "metadata": literal},
		{"nullable": false, "$ref": "resource", "optional": nil},
	} {
		if err := agentcontract.ValidateSchema(validator, value); err != nil {
			t.Fatalf("valid literal rejected: %v", err)
		}
	}
	if err := agentcontract.ValidateSchema(validator, agentcontract.JSON{"nullable": "wrong type", "$ref": "resource"}); err == nil {
		t.Fatal("property validation was removed")
	}
}

func TestOpenAPIImportsArrayScalarAndEmptyResponses(t *testing.T) {
	doc := agentcontract.JSON{"openapi": "3.1.0", "components": agentcontract.JSON{"schemas": agentcontract.JSON{"Ids": agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "string"}}}}, "paths": agentcontract.JSON{
		"/ids":    agentcontract.JSON{"get": agentcontract.JSON{"operationId": "listIds", "responses": agentcontract.JSON{"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": agentcontract.JSON{"$ref": "#/components/schemas/Ids"}}}}}}},
		"/count":  agentcontract.JSON{"get": agentcontract.JSON{"operationId": "getCount", "responses": agentcontract.JSON{"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": agentcontract.JSON{"type": "integer"}}}}}}},
		"/delete": agentcontract.JSON{"delete": agentcontract.JSON{"operationId": "deleteItem", "responses": agentcontract.JSON{"204": agentcontract.JSON{"description": "Deleted"}}}},
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
		validator, err := agentcontract.ValidateLocalSchema(output, true)
		if err != nil {
			t.Fatal(err)
		}
		valid := any([]any{"A"})
		invalid := any(json.Number("1"))
		if index == 1 {
			valid, invalid = json.Number("1"), []any{"A"}
		}
		if agentcontract.ValidateSchema(validator, agentcontract.JSON{"result": valid}) != nil || agentcontract.ValidateSchema(validator, agentcontract.JSON{"result": invalid}) == nil {
			t.Fatalf("bad generated output schema: %v", output)
		}
	}
	if caps[2].(map[string]any)["allow_empty_success"] != true {
		t.Fatalf("204 was not enabled: %v", caps[2])
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ids":
			if _, callErr := w.Write([]byte(`["A","B"]`)); callErr != nil {
				t.Error(callErr)
			}
		case "/count":
			if _, callErr2 := w.Write([]byte(`2`)); callErr2 != nil {
				t.Error(callErr2)
			}
		case "/delete":
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	pack, err := LoadRestPack(writeTestManifest(t, draft), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(pack.Close)
	for _, tc := range []struct {
		name string
		want agentcontract.JSON
	}{
		{"listIds", agentcontract.JSON{"result": []any{"A", "B"}}},
		{"getCount", agentcontract.JSON{"result": json.Number("2")}},
		{"deleteItem", agentcontract.JSON{}},
	} {
		result, err := pack.Invoke(context.Background(), tc.name, agentcontract.JSON{}, nil)
		if err != nil || result.ErrorCode != "" || !reflect.DeepEqual(result.Data, tc.want) {
			t.Fatalf("%s: result=%+v err=%v want=%v", tc.name, result, err, tc.want)
		}
	}
}

func TestOpenAPIMixedSuccessShapesRequireManualMapping(t *testing.T) {
	doc := agentcontract.JSON{"openapi": "3.1.0", "paths": agentcontract.JSON{"/records": agentcontract.JSON{"get": agentcontract.JSON{
		"operationId": "listRecords",
		"responses": agentcontract.JSON{
			"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "string"}}}}},
			"204": agentcontract.JSON{"description": "No records"},
		},
	}}}}
	_, err := ImportOpenAPIDocument(doc, "records", "API_URL", []string{"listRecords"}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "listRecords: differing 2xx response schemas need manual mapping") {
		t.Fatalf("mixed 2xx responses silently accepted: %v", err)
	}
}
