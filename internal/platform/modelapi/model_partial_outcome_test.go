package modelapi

import (
	"bytes"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestPartialOutcomeKeepsItsOmissionWarningBesideTheResult(t *testing.T) {
	for _, mode := range []string{"", "json_object", "json_schema", "output_tools"} {
		for _, view := range []string{"partial", "full", "no_fact", "different_fact", "failed", "inspected", "steering"} {
			t.Run(mode+"/"+view, func(t *testing.T) {
				id := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
				fact := agentcontract.FactView{Fact: agentcontract.Fact{FactID: id, Value: agentcontract.JSON{"data": agentcontract.JSON{"count": 0, "enabled": false}}},
					OmittedPaths: [][]any{{"data", "hidden"}}}
				observation := agentcontract.Observation{CallRef: "read-1", Capability: "records.read", Arguments: agentcontract.JSON{"id": 23}, Status: "succeeded", FactID: &id}
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Observations: []agentcontract.Observation{observation}, Facts: []agentcontract.FactView{fact}}
				wantWarning := view == "partial" || view == "failed"
				switch view {
				case "full":
					packet.Facts[0].OmittedPaths = nil
				case "no_fact":
					packet.Facts = nil
				case "different_fact":
					packet.Facts[0].FactID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
				case "failed":
					packet.Observations[0].Status = "failed"
					packet.Observations[0].ErrorCode = agentcontract.Strptr("business_failed")
				case "inspected":
					packet.InspectedFact = agentcontract.JSON{"fact_id": id, "path": []any{"data"}, "preview": agentcontract.JSON{"value": agentcontract.JSON{"count": 0, "enabled": false}}, "omitted_paths": []any{}}
				case "steering":
					packet.Followups = []string{"steering: Read only the already visible count."}
				}
				input, err := agentcontract.CanonicalJSON(packet)
				if err != nil {
					t.Fatal(err)
				}
				model := &HTTPJSONDecisionModel{Model: "compatible-test-model", DecisionOutputMode: mode}
				payload, err := model.requestPayload(input, "Use recorded evidence.")
				if err != nil {
					t.Fatal(err)
				}
				messages := payload["messages"].([]any)
				last := messages[len(messages)-1].(agentcontract.JSON)["content"].(string)
				warning := strings.Contains(last, "The preceding saved Fact is a partial preview.")
				if warning != wantWarning {
					t.Fatalf("partial evidence warning=%t, want=%t", warning, wantWarning)
				}
				if wantWarning {
					if !strings.Contains(last, `"count":0`) || !strings.Contains(last, `"enabled":false`) || !strings.Contains(last, `"omitted_paths":[["data","hidden"]]`) {
						t.Fatal("warning replaced or changed the actual projected evidence")
					}
					if strings.Index(last, "The preceding saved Fact") <= strings.Index(last, `"omitted_paths"`) {
						t.Fatal("omission warning is separated from its preceding result")
					}
				}
				if view == "failed" && (!strings.Contains(last, `"status":"failed"`) || !strings.Contains(last, `"error_code":"business_failed"`)) {
					t.Fatal("partial evidence warning concealed a business failure")
				}
				after, err := agentcontract.CanonicalJSON(packet)
				if err != nil || !bytes.Equal(input, after) {
					t.Fatal("model presentation changed the caller's evidence", err)
				}
			})
		}
	}
}
