package modelapi

import (
	"strconv"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestShippedReviewModesOmitUnavailableRuntimeExamples(t *testing.T) {
	for _, search := range []bool{false, true} {
		runtime := &reactcore.AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}}
		packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft", FactIDs: []string{}}}
		if search {
			runtime.MaxContextCapabilities = 1
			packet.RuntimeFeatures = []string{"capability_search"}
		}
		input, err := agentcontract.CanonicalJSON(packet)
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"json_object", "json_schema", "output_tools"} {
			t.Run(mode+"/search="+strconv.FormatBool(search), func(t *testing.T) {
				model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
				payload, err := model.requestPayload(input, runtime.SystemPrompt())
				if err != nil {
					t.Fatal(err)
				}
				prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
				for _, unavailable := range []string{`"kind":"inspect_fact"`, `"kind":"search_capabilities"`, "Inspect only one needed path", "Use a standalone search_capabilities decision"} {
					if strings.Contains(prompt, unavailable) {
						t.Fatal("completion review advertises an unavailable decision", unavailable)
					}
				}
				for _, retained := range []string{"completion_feedback", "Before final, check every requested action", "Never invent IDs", "Never add requirements beyond the original request"} {
					// Output tools replace the JSON review paragraph with their own
					// final-only guidance, retaining the same scope boundary.
					if mode == "output_tools" && retained == "Never add requirements beyond the original request" {
						retained = "Do not invent missing requirements"
					}
					if !strings.Contains(prompt, retained) {
						t.Fatal("review lost an evidence or original-scope rule", retained)
					}
				}
				encoded, err := agentcontract.CanonicalJSON(payload)
				if err != nil {
					t.Fatal(err)
				}
				measurement, err := model.MeasureInput(packet, runtime.SystemPrompt())
				if err != nil || measurement.Tokens != int64(len(encoded)+128) {
					t.Fatal("review prompt is not fully measured", measurement, err)
				}
			})
		}
	}
}
