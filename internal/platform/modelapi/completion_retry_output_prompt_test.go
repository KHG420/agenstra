package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletionReviewRetryDoesNotForbidRequiredOutputTool(t *testing.T) {
	// This is the capitalized restriction emitted by the runtime after an invalid
	// review. A structured output tool is not a new business capability call.
	const retry = "Your previous review was invalid. Return exactly one final agenstra.decision.v1 JSON decision with completion_feedback as a string: identify missing originally requested execution, or use an empty string when work is handled or concretely blocked. No tools or other decision kinds are allowed during completion review."
	packet := agentcontract.ContextPacket{
		Schema:           "agenstra.context.v1",
		CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "proposed", FactIDs: []string{}},
	}
	input, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"json_object", "output_tools"} {
		t.Run(mode, func(t *testing.T) {
			model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
			payload, err := model.requestPayload(input, retry)
			if err != nil {
				t.Fatal(err)
			}
			messages := payload["messages"].([]any)
			prompt := messages[0].(agentcontract.JSON)["content"].(string)
			if mode == "json_object" {
				if !strings.Contains(prompt, "No tools or other decision kinds") || payload["tools"] != nil {
					t.Fatal("content-only review protocol changed")
				}
				return
			}
			if strings.Contains(prompt, "No tools or other decision kinds") {
				t.Fatal("retry review forbids its required structured output tool")
			}
			for _, rule := range []string{"No capability calls or other decision kinds", "Call submit_decision exactly once", "completion_feedback as a string", "concretely blocked"} {
				if !strings.Contains(prompt, rule) {
					t.Fatalf("retry constraint lost: %s", rule)
				}
			}
			assertSingleDecisionOutputRequest(t, payload)
			schemas := outputDecisionSchemas(t, payload)
			if len(schemas) != 1 || schemas["final"] == nil {
				t.Fatal("retry enabled a non-final decision")
			}
			assertReviewProposalInputPreserved(t, payload, input)
		})
	}
}
