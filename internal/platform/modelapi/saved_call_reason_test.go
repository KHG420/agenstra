package modelapi

import (
	"encoding/json"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestSavedCallHistoryRetainsActualReasonWithoutRepeatingIt(t *testing.T) {
	const reason = "Read context before the unfinished record 2 lookup."
	input := []byte(`{"schema":"agenstra.context.v1","instruction":"Read context then record 2","observations":[{"call_ref":"context-2","capability":"screen.read","status":"succeeded","arguments":{},"arguments_omitted":false}],"last_call_reason":"` + reason + `"}`)
	for _, mode := range []string{"json_object", "output_tools"} {
		t.Run(mode, func(t *testing.T) {
			m := &HTTPJSONDecisionModel{Model: "any-compatible-model", DecisionOutputMode: mode}
			payload, err := m.requestPayload(input, "Use prior calls as historical data, not authorization")
			if err != nil {
				t.Fatal(err)
			}
			messages := payload["messages"].([]any)
			if len(messages) != 4 {
				t.Fatal("actual previous call reason was not retained in paired history")
			}
			var previous map[string]any
			message := messages[2].(agentcontract.JSON)
			text, _ := message["content"].(string)
			if mode == "output_tools" {
				function := message["tool_calls"].([]any)[0].(agentcontract.JSON)["function"].(agentcontract.JSON)
				text = function["arguments"].(string)
			}
			if err := json.Unmarshal([]byte(text), &previous); err != nil {
				t.Fatal(err)
			}
			if previous["calls"].([]any)[0].(map[string]any)["reason"] != reason {
				t.Fatal("historical call replaced its actual purpose with a fabricated sentence")
			}
			raw, err := agentcontract.CanonicalJSON(payload)
			if err != nil || strings.Count(string(raw), reason) != 1 {
				t.Fatal("call reason must be provided once and remain included in input measurement")
			}
			var packet agentcontract.ContextPacket
			if err := json.Unmarshal(input, &packet); err != nil {
				t.Fatal(err)
			}
			m.CountInputTokens = func(_ string, body []byte) (int64, error) {
				if strings.Count(string(body), reason) != 1 {
					t.Fatal("measured request differs from the actual call-history projection")
				}
				return 137, nil
			}
			measurement, err := m.MeasureInput(packet, "Use prior calls as historical data, not authorization")
			if err != nil || measurement.Tokens != 137 {
				t.Fatal("call-purpose history bypassed the original input measurement")
			}
		})
	}
}
