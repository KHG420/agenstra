package capability

import (
	"bytes"
	"encoding/json"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestOpenAPIParameterDescriptionsReachTheToolWithoutMutatingTheSource(t *testing.T) {
	doc := agentcontract.JSON{"openapi": "3.0.3", "paths": agentcontract.JSON{
		"/records/{tenant}/{record}": agentcontract.JSON{"get": agentcontract.JSON{
			"operationId": "read_record", "parameters": []any{
				agentcontract.JSON{"name": "tenant", "in": "path", "required": true, "description": "Tenant slug", "schema": agentcontract.JSON{"type": "string"}},
				agentcontract.JSON{"name": "record", "in": "path", "required": true, "description": "Record locator", "schema": agentcontract.JSON{"type": "string", "description": "Record UUID"}},
			},
			"responses": agentcontract.JSON{"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": agentcontract.JSON{"type": "object"}}}}},
		}},
	}}
	before, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := ImportOpenAPIDocument(doc, "records", "RECORDS_URL", []string{"read_record"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	input := draft["capabilities"].([]any)[0].(map[string]any)["input_schema"].(map[string]any)
	path := input["properties"].(map[string]any)["path"].(map[string]any)["properties"].(map[string]any)
	if got := path["tenant"].(map[string]any)["description"]; got != "Tenant slug" {
		t.Fatalf("parameter description lost: %v", got)
	}
	if got := path["record"].(map[string]any)["description"]; got != "Record UUID" {
		t.Fatalf("existing schema description replaced: %v", got)
	}
	after, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("import mutated the caller's OpenAPI source")
	}
}

func TestOpenAPIReferencedParameterDescriptionPreservesValidation(t *testing.T) {
	for _, version := range []string{"3.0.3", "3.1.0"} {
		t.Run(version, func(t *testing.T) {
			doc := agentcontract.JSON{
				"openapi": version,
				"components": agentcontract.JSON{"schemas": agentcontract.JSON{
					"Tenant": agentcontract.JSON{"type": "string", "pattern": "^[a-z-]+$", "minLength": 2},
				}},
				"paths": agentcontract.JSON{"/records/{tenant}": agentcontract.JSON{"get": agentcontract.JSON{
					"operationId": "read_record", "parameters": []any{agentcontract.JSON{
						"name": "tenant", "in": "path", "required": true, "description": "Tenant slug",
						"schema": agentcontract.JSON{"$ref": "#/components/schemas/Tenant"},
					}},
					"responses": agentcontract.JSON{"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": agentcontract.JSON{"type": "object"}}}}},
				}}},
			}
			before, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			draft, err := ImportOpenAPIDocument(doc, "records", "RECORDS_URL", []string{"read_record"}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			input := draft["capabilities"].([]any)[0].(map[string]any)["input_schema"].(map[string]any)
			property := input["properties"].(map[string]any)["path"].(map[string]any)["properties"].(map[string]any)["tenant"].(map[string]any)
			if property["description"] != "Tenant slug" {
				t.Fatal("reference parameter description lost")
			}
			validator, err := agentcontract.ValidateLocalSchema(input, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := agentcontract.ValidateSchema(validator, agentcontract.JSON{"path": agentcontract.JSON{"tenant": "team-a"}}); err != nil {
				t.Fatal(err)
			}
			if agentcontract.ValidateSchema(validator, agentcontract.JSON{"path": agentcontract.JSON{"tenant": "bad/value"}}) == nil {
				t.Fatal("reference constraints lost")
			}
			after, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("reference source mutated")
			}
		})
	}
}

func TestOpenAPIReferencedParameterDescriptionKeepsSerializationRestrictions(t *testing.T) {
	doc := agentcontract.JSON{
		"openapi": "3.1.0",
		"components": agentcontract.JSON{"schemas": agentcontract.JSON{
			"Values": agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "string"}},
		}},
		"paths": agentcontract.JSON{"/records/{values}": agentcontract.JSON{"get": agentcontract.JSON{
			"operationId": "read_record", "parameters": []any{agentcontract.JSON{
				"name": "values", "in": "path", "required": true, "description": "Record values",
				"schema": agentcontract.JSON{"$ref": "#/components/schemas/Values", "type": "array"},
			}},
			"responses": agentcontract.JSON{"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": agentcontract.JSON{"type": "object"}}}}},
		}}},
	}
	if _, err := ImportOpenAPIDocument(doc, "records", "RECORDS_URL", []string{"read_record"}, nil, ""); err == nil {
		t.Fatal("a description allowed unsupported complex path serialization")
	}
}
