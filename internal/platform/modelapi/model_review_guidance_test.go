package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/ext/capability"
)

func TestOutputToolsReviewGuidanceMatchesFinalOnlyStage(t *testing.T) {
	const hostRule = "Preserve a null business response; verify a separately requested read."
	prompt := capability.AgentPrompt(hostRule)
	packet := agentcontract.ContextPacket{
		Schema: "agenstra.context.v1", Skills: []agentcontract.JSON{{"name": "host.guide"}},
		CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "proposed", FactIDs: []string{}},
	}
	input, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	model := &HTTPJSONDecisionModel{Model: "test", DecisionOutputMode: "output_tools"}
	payload, err := model.requestPayload(input, prompt)
	if err != nil {
		t.Fatal(err)
	}
	got := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
	if strings.Contains(got, "Choose one typed decision at a time: tool_batch,") ||
		strings.Contains(got, "Available JSON decision for completion review:") {
		t.Fatal("review guidance contradicts the final-only output-tool contract")
	}
	for _, rule := range []string{hostRule, "final and approve_completion are the only available decision kinds", "Call submit_decision exactly once", "completion_feedback", "Use answer_json for requested raw JSON"} {
		if !strings.Contains(got, rule) {
			t.Fatalf("review lost a required host or output rule: %s", rule)
		}
	}
	schemas := outputDecisionSchemas(t, payload)
	if len(schemas) != 2 || schemas["final"] == nil || schemas["approve_completion"] == nil {
		t.Fatal("review offered an execution decision")
	}
	assertSingleDecisionOutputRequest(t, payload)
	assertReviewProposalInputPreserved(t, payload, input)
	model.DecisionOutputMode = "json_object"
	legacy, err := model.requestPayload(input, prompt)
	if err != nil || legacy["tools"] != nil || legacy["response_format"] == nil {
		t.Fatal("review prompt adjustment changed the JSON output channel", err)
	}
}
