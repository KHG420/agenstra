package modelapi

import (
	"encoding/json"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeReferenceCannotBypassClosedParameterObject(t *testing.T) {
	const reference = `{"$fact_value":{"fact_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","path":["data","id"]}}`
	for _, tc := range []struct {
		name, arguments string
		declaredReason  bool
		pattern         bool
		open            bool
		wantRejected    bool
	}{
		{name: "original reference", arguments: `{"id":` + reference + `}`},
		{name: "undeclared reason beside reference", arguments: `{"id":` + reference + `,"reason":"Read the record"}`, wantRejected: true},
		{name: "unrelated unknown field beside reference", arguments: `{"id":` + reference + `,"query":{}}`, wantRejected: true},
		{name: "declared business reason", arguments: `{"id":` + reference + `,"reason":"business value"}`, declaredReason: true},
		{name: "pattern property remains valid", arguments: `{"id":` + reference + `,"label_note":"business value"}`, pattern: true},
		{name: "unmatched pattern property remains rejected", arguments: `{"id":` + reference + `,"query":{}}`, pattern: true, wantRejected: true},
		{name: "open object remains valid", arguments: `{"id":` + reference + `,"other":"business value"}`, open: true},
		{name: "whole argument reference remains symbolic", arguments: reference},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packet := nativeTestPacket()
			schema := packet.Capabilities[0]["input_schema"].(agentcontract.JSON)
			if tc.declaredReason {
				schema["properties"].(agentcontract.JSON)["reason"] = agentcontract.JSON{"type": "string"}
			}
			if tc.pattern {
				schema["patternProperties"] = agentcontract.JSON{"^label_": agentcontract.JSON{"type": "string"}}
			}
			if tc.open {
				schema["additionalProperties"] = true
			}
			input, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			model := &HTTPJSONDecisionModel{Model: "test", DecisionOutputMode: "native_tools"}
			payload, err := model.requestPayload(input, "Use original business parameters without adding metadata.")
			if err != nil {
				t.Fatal(err)
			}
			function := nativeTestFunction(t, payload, "inventory.get")
			call := decisionOutputToolCall{Type: "function"}
			call.Function.Name, call.Function.Arguments = function["name"].(string), tc.arguments
			body, detail := nativeDecisionOutputToolBody([]decisionOutputToolCall{call}, payload["tools"].([]any), packet)
			if tc.wantRejected {
				if detail != "model_decision_schema_invalid" || body != nil {
					t.Fatal("reference bypassed the declared parameter object", string(body), detail)
				}
				return
			}
			if detail != "" {
				t.Fatal("valid symbolic business arguments were rejected", detail)
			}
			decision, err := agentcontract.StrictDecision(body)
			if err != nil {
				t.Fatal(err)
			}
			var expected agentcontract.JSON
			if err := json.Unmarshal([]byte(tc.arguments), &expected); err != nil {
				t.Fatal(err)
			}
			got, err := agentcontract.CanonicalJSON(decision.Calls[0].Arguments)
			if err != nil {
				t.Fatal(err)
			}
			want, err := agentcontract.CanonicalJSON(expected)
			if err != nil || string(got) != string(want) {
				t.Fatal("valid symbolic arguments were changed", string(got), err)
			}
		})
	}
}
