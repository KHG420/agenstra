package modelapi

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeToolsPreserveOriginalSchemaResourcesAndConditions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		schema, valid agentcontract.JSON
	}{
		{"nested resource", agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"record": agentcontract.JSON{"$id": "urn:test:nested-input", "type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "integer"}}, "required": []string{"id"}, "additionalProperties": false}}, "required": []string{"record"}, "additionalProperties": false}, agentcontract.JSON{"record": agentcontract.JSON{"id": 7}}},
		{"conditional input", agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "integer"}, "note": agentcontract.JSON{"type": "string"}}, "required": []string{"id"}, "additionalProperties": false, "if": agentcontract.JSON{"properties": agentcontract.JSON{"id": agentcontract.JSON{"const": 1}}}, "then": agentcontract.JSON{"required": []string{"note"}}}, agentcontract.JSON{"id": 7}},
		{"recursive definition", agentcontract.JSON{"type": "object", "$defs": agentcontract.JSON{"record": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "integer"}, "child": agentcontract.JSON{"$ref": "#/$defs/record"}}, "required": []string{"id"}, "additionalProperties": false}}, "properties": agentcontract.JSON{"record": agentcontract.JSON{"$ref": "#/$defs/record"}}, "required": []string{"record"}, "additionalProperties": false}, agentcontract.JSON{"record": agentcontract.JSON{"id": 7, "child": agentcontract.JSON{"id": 8}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, err := agentcontract.ValidateLocalSchema(tc.schema, false)
			if err != nil || agentcontract.ValidateSchema(original, tc.valid) != nil {
				t.Fatal("invalid original input fixture", err)
			}
			packet := nativeTestPacket()
			packet.Capabilities[0]["input_schema"] = tc.schema
			raw, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "native_tools"}
			payload, err := model.requestPayload(raw, "Preserve the declared input contract.")
			if err != nil {
				t.Fatal(err)
			}
			parameters := nativeTestFunction(t, payload, "inventory.get")["parameters"].(agentcontract.JSON)
			compiled, err := agentcontract.ValidateLocalSchema(parameters, false)
			if err != nil || agentcontract.ValidateSchema(compiled, tc.valid) != nil {
				t.Fatal("a valid original input stopped working in the native declaration", err)
			}
			want, err := agentcontract.CanonicalJSON(tc.schema)
			if err != nil {
				t.Fatal(err)
			}
			got, err := agentcontract.CanonicalJSON(parameters)
			if err != nil || string(want) != string(got) {
				t.Fatal("native declaration rewrote the original business schema", err)
			}
		})
	}
}
