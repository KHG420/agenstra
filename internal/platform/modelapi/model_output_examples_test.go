package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/ext/capability"
)

func TestOutputToolExamplesMatchDeclaredWireFields(t *testing.T) {
	const search = `Use a standalone search_capabilities decision. Example: {"schema":"agenstra.decision.v1","kind":"search_capabilities","query":"records"}. Search never grants permission.`
	const hostRule = "Keep host approval and recorded evidence rules."
	for _, mode := range []string{"output_tools", "json_object", "json_schema"} {
		t.Run(mode, func(t *testing.T) {
			m := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", RuntimeFeatures: []string{"capability_search"}}
			input, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := m.requestPayload(input, capability.AgentPrompt(hostRule)+"\n"+search)
			if err != nil {
				t.Fatal(err)
			}
			prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
			if !strings.Contains(prompt, hostRule) || !strings.Contains(prompt, "Search never grants permission.") {
				t.Fatal("protocol adaptation removed authorization rules")
			}
			hasJSONEnvelope := strings.Contains(prompt, `"schema":"agenstra.decision.v1"`)
			if mode != "output_tools" {
				if !hasJSONEnvelope {
					t.Fatal("JSON mode lost its required schema envelope")
				}
				return
			}
			if hasJSONEnvelope {
				t.Fatal("output-tool prompt teaches the schema field forbidden by its declared parameters")
			}
			for _, field := range []string{"inspect_fact: required kind (string), fact_id (string), path (array)", "Call submit_decision exactly once", "search_capabilities: required"} {
				if !strings.Contains(prompt, field) {
					t.Fatalf("available typed decision guidance missing: %s", field)
				}
			}
			assertSingleDecisionOutputRequest(t, payload)
		})
	}
}
