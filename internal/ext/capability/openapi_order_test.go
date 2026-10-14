package capability

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestOpenAPIParameterContractsHaveStableRequiredOrder(t *testing.T) {
	parameters := []any{}
	for _, location := range []string{"path", "query"} {
		for _, name := range []string{"zeta", "alpha", "middle"} {
			parameters = append(parameters, agentcontract.JSON{"name": name, "in": location, "required": true, "schema": agentcontract.JSON{"type": "string"}})
		}
	}
	parameters = append(parameters, agentcontract.JSON{"name": "optional", "in": "query", "schema": agentcontract.JSON{"type": "string"}})
	doc := agentcontract.JSON{"openapi": "3.0.3", "paths": agentcontract.JSON{
		"/records/{zeta}/{alpha}/{middle}": agentcontract.JSON{"get": agentcontract.JSON{
			"operationId": "read_records", "parameters": parameters,
			"responses": agentcontract.JSON{"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{
				"schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}},
			}}}},
		}},
	}}
	var first []byte
	for attempt := range 64 {
		draft, err := ImportOpenAPIDocument(doc, "records", "RECORDS_URL", []string{"read_records"}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		input := draft["capabilities"].([]any)[0].(map[string]any)["input_schema"].(map[string]any)
		properties := input["properties"].(map[string]any)
		for _, location := range []string{"path", "query"} {
			required := properties[location].(map[string]any)["required"]
			if !reflect.DeepEqual(required, []any{"alpha", "middle", "zeta"}) {
				t.Fatalf("attempt %d %s required order: %#v", attempt, location, required)
			}
		}
		encoded, err := json.Marshal(draft)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			first = encoded
		} else if !bytes.Equal(first, encoded) {
			t.Fatalf("identical OpenAPI source produced different manifest bytes on attempt %d", attempt)
		}
	}
}
