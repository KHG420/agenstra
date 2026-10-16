package modelapi

import (
	"encoding/json"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeToolsSavedCallUsesItsDeclaredFunctionParameters(t *testing.T) {
	for _, tc := range []struct {
		name, apiType, thinking string
		omitted, deferred       bool
		wantPair                bool
	}{
		{name: "compatible native history", apiType: "compatible_chat", wantPair: true},
		{name: "thinking explicitly disabled", apiType: "deepseek_chat", thinking: "disabled", wantPair: true},
		{name: "reasoning not retained", apiType: "deepseek_chat"},
		{name: "arguments omitted", apiType: "compatible_chat", omitted: true},
		{name: "contract currently deferred", apiType: "compatible_chat", deferred: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packet := nativeTestPacket()
			if tc.deferred {
				packet.Capabilities[0]["schema_requires_inspection"] = true
			}
			id := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			packet.Observations = []agentcontract.Observation{{CallRef: "previous-call", Capability: "inventory.get", Status: "succeeded", FactID: &id, Arguments: agentcontract.JSON{"id": 7}, ArgumentsOmitted: tc.omitted}}
			packet.Facts = []agentcontract.FactView{{Fact: agentcontract.Fact{FactID: id, Value: agentcontract.JSON{"data": agentcontract.JSON{"id": 7, "enabled": false}}}, ReferenceAvailable: true}}
			packet.LastCallReason = "Previously selected operation reason must not become a business parameter."
			raw, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			model := &HTTPJSONDecisionModel{Model: "test", APIType: tc.apiType, Thinking: tc.thinking, DecisionOutputMode: "native_tools"}
			payload, err := model.requestPayload(raw, "Host instructions remain in force.")
			if err != nil {
				t.Fatal(err)
			}
			messages := payload["messages"].([]any)
			pairs := 0
			for i, item := range messages {
				message := item.(agentcontract.JSON)
				if message["role"] != "assistant" || message["tool_calls"] == nil {
					continue
				}
				pairs++
				calls := message["tool_calls"].([]any)
				call := calls[0].(agentcontract.JSON)
				function := call["function"].(agentcontract.JSON)
				var args agentcontract.JSON
				if len(calls) != 1 || function["name"] != nativeCapabilityFunctionName("inventory.get") || json.Unmarshal([]byte(function["arguments"].(string)), &args) != nil || len(args) != 1 || args["id"] != float64(7) {
					t.Fatal("saved native input included a decision wrapper or lost business parameters", function)
				}
				if i+1 >= len(messages) {
					t.Fatal("saved native call has no result")
				}
				result := messages[i+1].(agentcontract.JSON)
				if result["role"] != "tool" || result["tool_call_id"] != call["id"] || !strings.Contains(result["content"].(string), id) || !strings.Contains(result["content"].(string), `"enabled":false`) {
					t.Fatal("saved tool result identity or bounded evidence lost", result)
				}
			}
			if (pairs == 1) != tc.wantPair || pairs > 1 {
				t.Fatalf("native history pairs = %d, want pair %v", pairs, tc.wantPair)
			}
			after, err := agentcontract.CanonicalJSON(packet)
			if err != nil || string(after) != string(raw) {
				t.Fatal("native history mutated caller data", err)
			}
		})
	}
}

func TestNativeEmptyParametersDoNotAdvertiseUnusedReferenceDefinitions(t *testing.T) {
	for _, input := range []agentcontract.JSON{
		{"type": "object", "additionalProperties": false},
		{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
		{"type": "object", "properties": agentcontract.JSON{}, "$defs": agentcontract.JSON{"business": agentcontract.JSON{"type": "string"}}, "additionalProperties": false},
	} {
		before, err := agentcontract.CanonicalJSON(input)
		if err != nil {
			t.Fatal(err)
		}
		output, err := nativeCapabilityParameters(input)
		if err != nil {
			t.Fatal(err)
		}
		after, err := agentcontract.CanonicalJSON(output)
		if err != nil || string(before) != string(after) {
			t.Fatal("parameterless function gained unused reference syntax", string(after), err)
		}
	}
}
