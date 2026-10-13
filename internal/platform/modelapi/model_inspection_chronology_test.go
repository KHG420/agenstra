package modelapi

import (
	"bytes"
	"testing"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCurrentInspectionDoesNotReplayOlderPreviewAfterIt(t *testing.T) {
	for _, mode := range []string{"", "json_object", "json_schema", "output_tools"} {
		for _, view := range []string{"complete", "partial", "null", "different_fact", "failed", "no_value"} {
			t.Run(mode+"/"+view, func(t *testing.T) {
				id := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
				inspection := agentcontract.JSON{"fact_id": id, "path": []any{"data", "entries"},
					"preview": agentcontract.JSON{"value": agentcontract.JSON{"record": agentcontract.JSON{"id": 23, "label": "Complete saved label"}}}, "omitted_paths": []any{}}
				switch view {
				case "partial":
					inspection["omitted_paths"] = []any{[]any{"value", "other-record"}}
				case "null":
					inspection["preview"] = agentcontract.JSON{"value": nil}
				case "different_fact":
					inspection["fact_id"] = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
				case "failed":
					inspection["error_code"] = "fact_reference_path_invalid"
				case "no_value":
					inspection["preview"] = agentcontract.JSON{}
				}
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Return the saved entries unchanged.",
					Observations: []agentcontract.Observation{{CallRef: "read-1", Capability: "records.read", Status: "succeeded", Arguments: agentcontract.JSON{}, FactID: &id}},
					Facts: []agentcontract.FactView{{Fact: agentcontract.Fact{FactID: id, Value: agentcontract.JSON{"data": agentcontract.JSON{"entries": agentcontract.JSON{"record": agentcontract.JSON{"id": 23}}}}},
						OmittedPaths: [][]any{{"data", "entries", "record", "label"}}, ReferenceAvailable: true}}, InspectedFact: inspection}
				input, err := agentcontract.CanonicalJSON(packet)
				if err != nil {
					t.Fatal(err)
				}
				model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
				payload, err := model.requestPayload(input, "Continue from the latest saved evidence.")
				if err != nil {
					t.Fatal(err)
				}
				messages := payload["messages"].([]any)
				want := 4
				if view == "complete" || view == "partial" || view == "null" {
					want = 2
				}
				if len(messages) != want {
					t.Fatal("older business preview was replayed after its inspection, or an unrelated outcome disappeared", len(messages), want)
				}
				content := messages[1].(agentcontract.JSON)["content"].(string)
				if view == "complete" || view == "null" {
					var wire agentcontract.ContextPacket
					if err := jsonvalue.DecodeStrict([]byte(content), &wire); err != nil {
						t.Fatal(err)
					}
					if _, err := agentcontract.ValueAt(wire.Facts[0].Value, []any{"data", "entries"}); err == nil || len(wire.Facts[0].OmittedPaths) != 2 {
						t.Fatal("complete inspection kept the overlapping partial preview or lost its omission")
					}
					for _, pair := range [][2]any{{wire.InspectedFact, packet.InspectedFact}, {wire.Observations, packet.Observations}} {
						a, aErr := agentcontract.CanonicalJSON(pair[0])
						b, bErr := agentcontract.CanonicalJSON(pair[1])
						if aErr != nil || bErr != nil || !bytes.Equal(a, b) {
							t.Fatal("suppressing duplicate preview lost original observation or inspected data", aErr, bErr)
						}
					}
				} else if content != string(input) {
					t.Fatal("partial, unrelated or unverified inspection changed the original packet")
				}
				raw, err := agentcontract.CanonicalJSON(payload)
				if err != nil {
					t.Fatal(err)
				}
				measurement, err := model.MeasureInput(packet, "Continue from the latest saved evidence.")
				if err != nil || measurement.Tokens != int64(len(raw)+128) {
					t.Fatal("inspection request escaped actual input measurement", measurement, err)
				}
				after, err := agentcontract.CanonicalJSON(packet)
				if err != nil || !bytes.Equal(input, after) {
					t.Fatal("request construction mutated saved evidence", err)
				}
			})
		}
	}
}
