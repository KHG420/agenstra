package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestDecisionOutputToolsSearchMatchesRuntimeFeature(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features []string
		want     bool
	}{
		{name: "disabled"},
		{name: "durable execution only", features: []string{"durable_execution"}},
		{name: "enabled with deferred catalog", features: []string{"capability_search"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "output_tools"}
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", RuntimeFeatures: tc.features, CapabilityCatalogTotal: 20}
			raw, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := model.requestPayload(raw, "Choose one decision at a time.")
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, tool := range payload["tools"].([]any) {
				names[tool.(agentcontract.JSON)["function"].(agentcontract.JSON)["name"].(string)] = true
			}
			prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
			if names["submit_search_capabilities"] != tc.want || strings.Contains(prompt, "submit_search_capabilities: required") != tc.want {
				t.Fatalf("search tool and guidance do not match runtime availability: tools=%v, want=%v", names, tc.want)
			}
			for _, name := range []string{"submit_tool_call", "submit_tool_batch", "submit_final", "submit_request_input", "submit_inspect_capability", "submit_inspect_fact"} {
				if !names[name] {
					t.Fatalf("unrelated output decision was removed: %s", name)
				}
			}
			if payload["tool_choice"] != "required" || payload["parallel_tool_calls"] != false {
				t.Fatal("single-decision output constraints changed")
			}
		})
	}
}
