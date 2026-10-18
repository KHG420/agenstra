package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeSingleReferenceHistoryUsesSingleProposal(t *testing.T) {
	sourceID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	resultID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	reference := agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": sourceID, "path": []any{"data", "id"}}}
	for _, nested := range []bool{false, true} {
		for _, apiType := range []string{"compatible_chat", "deepseek_chat"} {
			name := apiType + map[bool]string{false: "/scalar", true: "/nested-array"}[nested]
			t.Run(name, func(t *testing.T) {
				packet := nativeTestPacket()
				arguments := agentcontract.JSON{"id": reference}
				if nested {
					arguments = agentcontract.JSON{"ids": []any{7, reference, reference}}
					packet.Capabilities[0]["input_schema"] = agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
						"ids": agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "integer"}},
					}, "required": []string{"ids"}, "additionalProperties": false}
				}
				packet.LastCallReason = "Use the requested earlier source once."
				packet.Observations = []agentcontract.Observation{{CallRef: "previous-call", Capability: "inventory.get", Status: "succeeded", FactID: &resultID, Arguments: arguments}}
				packet.Facts = []agentcontract.FactView{
					{Fact: agentcontract.Fact{FactID: sourceID, Value: agentcontract.JSON{"data": agentcontract.JSON{"id": 7}}}, ReferenceAvailable: true},
					{Fact: agentcontract.Fact{FactID: resultID, Value: agentcontract.JSON{"data": agentcontract.JSON{"enabled": false, "tokens": 0}}}, ReferenceAvailable: true},
				}
				before, err := agentcontract.CanonicalJSON(packet)
				if err != nil {
					t.Fatal(err)
				}
				model := &HTTPJSONDecisionModel{Model: "test", APIType: apiType, DecisionOutputMode: "native_tools"}
				if apiType == "deepseek_chat" {
					model.Thinking = "disabled"
				}
				payload, err := model.requestPayload(before, "Keep the requested input sources and operation order.")
				if err != nil {
					t.Fatal(err)
				}
				pairs := 0
				messages := payload["messages"].([]any)
				for i, value := range messages {
					message := value.(agentcontract.JSON)
					if message["role"] != "assistant" || message["tool_calls"] == nil {
						continue
					}
					pairs++
					calls := message["tool_calls"].([]any)
					if len(calls) != 1 {
						t.Fatal("single saved operation gained additional calls")
					}
					call := calls[0].(agentcontract.JSON)
					function := call["function"].(agentcontract.JSON)
					want, err := agentcontract.CanonicalJSON(agentcontract.JSON{"capability": "inventory.get", "arguments": arguments, "reason": packet.LastCallReason})
					if err != nil {
						t.Fatal(err)
					}
					if function["name"] != "tool_call" || function["arguments"] != string(want) {
						t.Fatal("single symbolic history contradicts the declared single-proposal channel")
					}
					if i+1 >= len(messages) {
						t.Fatal("single saved proposal lost its result")
					}
					result := messages[i+1].(agentcontract.JSON)
					if result["role"] != "tool" || result["tool_call_id"] != call["id"] || !strings.Contains(result["content"].(string), resultID) || !strings.Contains(result["content"].(string), `"enabled":false`) || !strings.Contains(result["content"].(string), `"tokens":0`) {
						t.Fatal("saved proposal result lost its identity or actual values")
					}
				}
				after, err := agentcontract.CanonicalJSON(packet)
				if err != nil || string(after) != string(before) || pairs != 1 {
					t.Fatal("single-proposal history mutated the input or duplicated a call", err)
				}
				measured, err := model.MeasureInput(packet, "Keep the requested input sources and operation order.")
				encoded, encodeErr := agentcontract.CanonicalJSON(payload)
				if err != nil || encodeErr != nil || measured.Tokens != int64(len(encoded)+128) {
					t.Fatal("single-proposal history escaped request measurement", err, encodeErr)
				}
			})
		}
	}
}
