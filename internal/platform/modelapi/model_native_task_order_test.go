package modelapi

import (
	"encoding/json"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeCurrentTaskPrecedesItsRunEvidence(t *testing.T) {
	for _, protocol := range []struct{ apiType, thinking string }{
		{"compatible_chat", ""}, {"deepseek_chat", "enabled"}, {"deepseek_chat", "disabled"},
	} {
		for _, phase := range []string{"continuation", "steering", "completion_review"} {
			t.Run(protocol.apiType+"/"+protocol.thinking+"/"+phase, func(t *testing.T) {
				packet := nativeTestPacket()
				packet.Instruction = "Read this record once, then report the real result."
				packet.Observations = []agentcontract.Observation{{CallRef: "read-1", Capability: "inventory.get", Status: "succeeded",
					Arguments: agentcontract.JSON{"id": 7}, FactID: agentcontract.Strptr("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}}
				packet.Facts = []agentcontract.FactView{{Fact: agentcontract.Fact{FactID: *packet.Observations[0].FactID,
					Value: agentcontract.JSON{"data": agentcontract.JSON{"id": 7, "enabled": false, "missing": nil}}}, ReferenceAvailable: true}}
				switch phase {
				case "steering":
					packet.Followups = []string{"steering: Report the already obtained result."}
				case "completion_review":
					packet.CompletionReview = &agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Recorded result", FactIDs: []string{*packet.Observations[0].FactID}}
				}
				before, err := agentcontract.CanonicalJSON(packet)
				if err != nil {
					t.Fatal(err)
				}
				model := &HTTPJSONDecisionModel{Model: "any-compatible-model", APIType: protocol.apiType, Thinking: protocol.thinking, DecisionOutputMode: "native_tools"}
				payload, err := model.requestPayload(before, "Respect the task, evidence and live authorization.")
				if err != nil {
					t.Fatal(err)
				}
				messages := payload["messages"].([]any)
				if len(messages) < 3 || messages[1].(agentcontract.JSON)["role"] != "user" || messages[1].(agentcontract.JSON)["content"] != packet.Instruction {
					t.Fatal("the current task followed its own saved outcomes and looked like a new request")
				}
				var retained agentcontract.ContextPacket
				if err := json.Unmarshal([]byte(messages[2].(agentcontract.JSON)["content"].(string)), &retained); err != nil {
					t.Fatal("current-run evidence no longer follows its originating task", err)
				}
				if retained.Schema != packet.Schema || retained.Instruction != "" || len(retained.Observations) != 1 || retained.Observations[0].Status != "succeeded" || len(retained.Facts) != 1 || retained.Facts[0].FactID != packet.Facts[0].FactID {
					t.Fatal("reordering replaced current-run results or duplicated the task", retained)
				}
				if phase == "steering" && (len(retained.Followups) != 1 || retained.Followups[0] != packet.Followups[0]) {
					t.Fatal("reordering discarded the supplied followup")
				}
				after, err := agentcontract.CanonicalJSON(packet)
				if err != nil || string(before) != string(after) {
					t.Fatal("message reordering mutated caller evidence", err)
				}
			})
		}
	}
}
