package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

const expectedInitialCallShapeGuidance = `calls[] keys call_ref, capability, arguments, reason are siblings. Close arguments before reason. In arguments, match input_schema; omit unrequested {} or null fields.`

func TestJSONDecisionShapeIsAvailableBeforeFormatFailure(t *testing.T) {
	for _, mode := range []string{"", "json_object", "json_schema"} {
		for _, existingFeedback := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/initial", true: "/retained-feedback"}[existingFeedback], func(t *testing.T) {
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
				input, err := agentcontract.CanonicalJSON(packet)
				if err != nil {
					t.Fatal(err)
				}
				const hostRule = "Preserve host authorization and never replay a successful write."
				prompt := hostRule
				if existingFeedback {
					prompt += "\n" + agentcontract.DecisionObjectShapePrompt
				}
				model := &HTTPJSONDecisionModel{Model: "any-compatible-model", DecisionOutputMode: mode}
				payload, err := model.requestPayload(input, prompt)
				if err != nil {
					t.Fatal(err)
				}
				actual := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
				if strings.Count(actual, expectedInitialCallShapeGuidance) != 1 || !strings.Contains(actual, hostRule) {
					t.Fatal("JSON decision request must carry exactly one call-shape reminder before a provider error, preserving host policy")
				}
			})
		}
	}
}

func TestInitialDecisionShapeDoesNotTeachCallsDuringReviewOrMemory(t *testing.T) {
	for _, mode := range []string{"json_object", "json_schema", "output_tools"} {
		packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", CompletionReview: &agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Verified answer", FactIDs: []string{}}}
		input, err := agentcontract.CanonicalJSON(packet)
		if err != nil {
			t.Fatal(err)
		}
		model := &HTTPJSONDecisionModel{Model: "any-compatible-model", DecisionOutputMode: mode}
		payload, err := model.requestPayload(input, "Preserve review authority.\n"+agentcontract.DecisionObjectShapePrompt)
		if err != nil {
			t.Fatal(err)
		}
		actual := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
		if strings.Contains(actual, agentcontract.DecisionObjectShapePrompt) || strings.Contains(actual, expectedInitialCallShapeGuidance) {
			t.Fatalf("%s review retained a capability-call output example", mode)
		}
		payload, err = model.requestPayload([]byte(`{"text":"Remember a preference"}`), "Extract memory only.")
		if err != nil {
			t.Fatal(err)
		}
		actual = payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
		if strings.Contains(actual, agentcontract.DecisionObjectShapePrompt) || strings.Contains(actual, expectedInitialCallShapeGuidance) || payload["response_format"].(agentcontract.JSON)["type"] != "json_object" {
			t.Fatalf("%s memory extraction gained a decision protocol", mode)
		}
	}
}

func TestNestedCallMetadataStillRequiresItsDeclaredSibling(t *testing.T) {
	malformed := []byte(`{"schema":"agenstra.decision.v1","kind":"tool_batch","calls":[{"call_ref":"read-1","capability":"example.lookup","arguments":{"path":{"id":{"$fact_value":{"fact_id":"11111111-1111-4111-8111-111111111111","path":["data","id"]}}},"reason":"Read the record"}}]}`)
	schema := nativeDecisionOutputSchema(decisionOutputTools(false))
	if got := validateNativeDecisionOutput(malformed, schema); got != "model_decision_schema_invalid" {
		t.Fatalf("misplaced metadata must be rejected rather than moved out of business arguments: %s", got)
	}
}
