package modelapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompleteInspectionIsNotMixedWithItsPartialPreview(t *testing.T) {
	for _, mode := range []string{"", "json_object", "json_schema", "output_tools"} {
		for _, kind := range []string{"object", "array", "root", "null", "partial_inspection", "different_fact", "failed", "unknown_completeness", "unrelated_omission", "cheap_array", "array_page"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				id := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
				partial := agentcontract.JSON{"id": 23, "body": strings.Repeat("x", 500)}
				complete := agentcontract.JSON{"id": 23, "body": partial["body"], "label": "Complete saved value"}
				path := []any{"data", "entry"}
				value := agentcontract.JSON{"data": agentcontract.JSON{"entry": partial, "untouched": 0}}
				var inspected any = complete
				omit := true
				switch kind {
				case "array", "cheap_array", "array_page":
					path = []any{"data", "rows", 0}
					if kind == "cheap_array" {
						partial = agentcontract.JSON{"id": 23}
						omit = false
					}
					value = agentcontract.JSON{"data": agentcontract.JSON{"rows": []any{partial, false}, "untouched": 0}}
					if kind == "array_page" {
						path = []any{"data", "rows"}
						inspected = []any{complete}
						omit = false
					}
				case "root":
					path = []any{}
					inspected = agentcontract.JSON{"data": agentcontract.JSON{"entry": complete, "untouched": 0}}
				case "null":
					inspected = nil
				case "partial_inspection", "different_fact", "failed", "unknown_completeness", "unrelated_omission":
					omit = false
				}
				missing := append(append([]any{}, path...), "label")
				if kind == "array_page" {
					missing = []any{"data", "rows", 0, "label"}
				}
				inspection := agentcontract.JSON{"fact_id": id, "path": path, "preview": agentcontract.JSON{"value": inspected}, "omitted_paths": []any{}}
				fact := agentcontract.FactView{Fact: agentcontract.Fact{FactID: id, Value: value}, OmittedPaths: [][]any{missing}, ReferenceAvailable: false}
				switch kind {
				case "array_page":
					inspection["offset"], inspection["limit"], inspection["array_length"], inspection["returned_count"], inspection["next_offset"] = 0, 1, 2, 1, 1
				case "partial_inspection":
					inspection["omitted_paths"] = []any{[]any{"value", "other"}}
				case "different_fact":
					inspection["fact_id"] = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
				case "failed":
					inspection["error_code"] = "fact_reference_path_invalid"
				case "unknown_completeness":
					delete(inspection, "omitted_paths")
				case "unrelated_omission":
					fact.OmittedPaths = [][]any{{"other"}}
				}
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", InspectedFact: inspection, Facts: []agentcontract.FactView{fact},
					Observations: []agentcontract.Observation{{CallRef: "read-1", Capability: "records.read", Status: "succeeded", FactID: &id}}}
				input, err := agentcontract.CanonicalJSON(packet)
				if err != nil {
					t.Fatal(err)
				}
				model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
				payload, err := model.requestPayload(input, "Use the saved evidence.")
				if err != nil {
					t.Fatal(err)
				}
				body := payload["messages"].([]any)[1].(agentcontract.JSON)["content"].(string)
				var wire agentcontract.ContextPacket
				if err := jsonvalue.DecodeStrict([]byte(body), &wire); err != nil {
					t.Fatal(err)
				}
				if omit {
					if len(path) == 0 {
						if len(wire.Facts[0].Value) != 0 {
							t.Fatal("complete root inspection kept its partial copy")
						}
					} else if kind == "array" {
						rows, readErr := agentcontract.ValueAt(wire.Facts[0].Value, []any{"data", "rows"})
						if readErr != nil || len(rows.([]any)) != 2 || rows.([]any)[0] != nil || rows.([]any)[1] != false {
							t.Fatal("omitting an inspected element shifted array positions", rows, readErr)
						}
					} else if _, err := agentcontract.ValueAt(wire.Facts[0].Value, path); err == nil {
						t.Fatal("partial copy survived beside its complete inspection")
					}
					if len(wire.Facts[0].OmittedPaths) != 2 || len(body) >= len(input) {
						t.Fatal("omission was not explicit or exceeded the original input budget", wire.Facts[0].OmittedPaths)
					}
					if len(path) > 0 {
						zero, readErr := agentcontract.ValueAt(wire.Facts[0].Value, []any{"data", "untouched"})
						if readErr != nil || zero != json.Number("0") {
							t.Fatal("unrelated evidence changed", zero, readErr)
						}
					}
				} else if body != string(input) {
					t.Fatal("unverified, unrelated or cheap evidence was removed")
				}
				for _, pair := range [][2]any{{wire.InspectedFact, packet.InspectedFact}, {wire.Observations, packet.Observations}} {
					a, aErr := agentcontract.CanonicalJSON(pair[0])
					b, bErr := agentcontract.CanonicalJSON(pair[1])
					if aErr != nil || bErr != nil || !bytes.Equal(a, b) {
						t.Fatal("current inspection or business outcome changed", aErr, bErr)
					}
				}
				after, err := agentcontract.CanonicalJSON(packet)
				if err != nil || !bytes.Equal(input, after) || wire.Facts[0].FactID != id || wire.Facts[0].ReferenceAvailable {
					t.Fatal("input ownership, Fact identity or reference availability changed", err)
				}
			})
		}
	}
}
