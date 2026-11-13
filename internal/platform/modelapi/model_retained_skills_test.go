package modelapi

import (
	"slices"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/ext/capability"
)

func TestSkillReadChoicesExcludeOnlyRetainedContents(t *testing.T) {
	for _, mode := range []string{"output_tools", "native_tools", "json_schema"} {
		for _, retained := range []bool{false, true} {
			t.Run(mode+"/retained="+map[bool]string{false: "no", true: "yes"}[retained], func(t *testing.T) {
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1",
					Skills:           []agentcontract.JSON{{"name": "guide"}, {"name": "other"}},
					ContextOmissions: []string{"skill: other; read_skill to load again"},
				}
				if retained {
					packet.LoadedSkills = map[string]string{"guide": "Complete retained instructions."}
				}
				model := &HTTPJSONDecisionModel{Model: "test", DecisionOutputMode: mode}
				for _, allLoaded := range []bool{false, true} {
					if allLoaded {
						packet.LoadedSkills = map[string]string{"guide": "Complete guide.", "other": "Complete other guide."}
						packet.ContextOmissions = nil
					}
					input, err := agentcontract.CanonicalJSON(packet)
					if err != nil {
						t.Fatal(err)
					}
					payload, err := model.requestPayload(input, capability.AgentPrompt("Host rules still apply."))
					if err != nil {
						t.Fatal(err)
					}
					var schema agentcontract.JSON
					switch mode {
					case "output_tools":
						schema = outputDecisionSchemas(t, payload)["read_skill"]
					case "native_tools":
						for _, item := range payload["tools"].([]any) {
							function := item.(agentcontract.JSON)["function"].(agentcontract.JSON)
							if function["name"] == "read_skill" {
								schema = function["parameters"].(agentcontract.JSON)
							}
						}
					case "json_schema":
						root := payload["response_format"].(agentcontract.JSON)["json_schema"].(agentcontract.JSON)["schema"].(agentcontract.JSON)
						for _, item := range root["anyOf"].([]any) {
							variant := item.(agentcontract.JSON)
							if variant["properties"].(agentcontract.JSON)["kind"].(agentcontract.JSON)["const"] == "read_skill" {
								schema = variant
							}
						}
					}
					if allLoaded {
						if schema != nil {
							t.Fatal("a completed skill read is still offered when all content is retained")
						}
						continue
					}
					if schema == nil {
						t.Fatal("unread skill became unavailable")
					}
					want := []string{"guide", "other"}
					if retained {
						want = []string{"other"}
					}
					got, _ := schema["properties"].(agentcontract.JSON)["name"].(agentcontract.JSON)["enum"].([]string)
					if !slices.Equal(got, want) {
						t.Fatalf("skill read choices = %v, want %v", got, want)
					}
				}
			})
		}
	}
}
