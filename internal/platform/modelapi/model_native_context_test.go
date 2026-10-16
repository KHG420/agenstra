package modelapi

import (
	"encoding/json"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeToolsPresentTheOriginalTaskAsAUserMessage(t *testing.T) {
	packet := nativeTestPacket()
	packet.Instruction = "Read the record, using the user's requested prior $fact_value source.\nReturn JSON with the original fields."
	packet.Followups = []string{"The already supplied object ID is 7."}
	packet.Capabilities[0]["output_schema"] = agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"enabled": agentcontract.JSON{"type": "boolean"}}}
	packet.Capabilities[1]["input_schema"] = agentcontract.JSON{"type": "object", "description": "Partial view; inspection is required."}
	packet.InspectedCapability = packet.Capabilities[0]
	before, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "native_tools"}
	payload, err := model.requestPayload(before, "Keep the existing authorization and evidence rules.")
	if err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	if len(messages) != 3 || messages[2].(agentcontract.JSON)["role"] != "user" || messages[2].(agentcontract.JSON)["content"] != packet.Instruction {
		t.Fatal("unchanged task was not emitted as a native user message", messages)
	}
	var data agentcontract.JSON
	if err := json.Unmarshal([]byte(messages[1].(agentcontract.JSON)["content"].(string)), &data); err != nil {
		t.Fatal(err)
	}
	if _, duplicate := data["instruction"]; duplicate {
		t.Fatal("task was duplicated inside runtime data")
	}
	caps := data["capabilities"].([]any)
	if _, duplicate := caps[0].(agentcontract.JSON)["input_schema"]; duplicate {
		t.Fatal("native tool input contract was duplicated in runtime data")
	}
	if _, missing := caps[1].(agentcontract.JSON)["input_schema"]; !missing {
		t.Fatal("deferred contract data was removed without a declared function")
	}
	if _, duplicate := data["inspected_capability"].(agentcontract.JSON)["input_schema"]; duplicate {
		t.Fatal("inspected native contract was duplicated")
	}
	output, err := agentcontract.CanonicalJSON(caps[0].(agentcontract.JSON)["output_schema"])
	wantOutput, wantErr := agentcontract.CanonicalJSON(packet.Capabilities[0]["output_schema"])
	if err != nil || wantErr != nil || string(output) != string(wantOutput) || data["followups"].([]any)[0] != packet.Followups[0] {
		t.Fatal("output contract or supplied user data lost", data)
	}
	function := nativeTestFunction(t, payload, "inventory.get")
	schema, err := agentcontract.ValidateLocalSchema(function["parameters"].(agentcontract.JSON), false)
	if err != nil || agentcontract.ValidateSchema(schema, agentcontract.JSON{"id": 7}) != nil || agentcontract.ValidateSchema(schema, agentcontract.JSON{"id": "7"}) == nil {
		t.Fatal("the declared native input contract was lost or weakened", err)
	}
	after, err := agentcontract.CanonicalJSON(packet)
	if err != nil || string(after) != string(before) {
		t.Fatal("native message projection mutated caller-owned data", err)
	}
}

func TestNativeToolsSyntheticCheckKeepsNonemptyMessageSequence(t *testing.T) {
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "native_tools"}
	payload, err := model.requestPayload(raw, "Return a final answer with ok.")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range payload["messages"].([]any) {
		if text, ok := item.(agentcontract.JSON)["content"].(string); ok && text == "" {
			t.Fatal("synthetic model check gained an empty user message")
		}
	}
}
