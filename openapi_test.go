package agenstra

import (
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
