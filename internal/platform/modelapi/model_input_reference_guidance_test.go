package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestDecisionOutputModesRetainRequestedInputSourceGuidance(t *testing.T) {
	runtime := &reactcore.AgentRuntime{Provider: &coreTestProvider{}}
	packet := nativeTestPacket()
	packet.Instruction = "Use the earlier list result as the record ID input, even when the same ID is supplied here."
	input, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", "json_object", "json_schema", "output_tools", "native_tools"} {
		t.Run(mode, func(t *testing.T) {
			model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
			payload, err := model.requestPayload(input, runtime.SystemPrompt())
			if err != nil {
				t.Fatal(err)
			}
			prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
			for _, rule := range []string{"Use $fact_value for requested Fact/path inputs", "keep other supplied arguments exact."} {
				if strings.Count(prompt, rule) != 1 {
					t.Errorf("requested input-source rule missing or duplicated in %s: %q", mode, rule)
				}
			}
			if mode == "native_tools" {
				for _, rule := range []string{
					"A matching literal, including a value already present in the task, does not satisfy a requested input reference.",
					"Preserve the requested input source even when the same business value appears in another Fact.",
					"Final-answer citations alone do not require replacing ordinary literal inputs.",
				} {
					if strings.Count(prompt, rule) != 1 {
						t.Errorf("native reference-channel guidance missing or duplicated: %q", rule)
					}
				}
			}
		})
	}
}
