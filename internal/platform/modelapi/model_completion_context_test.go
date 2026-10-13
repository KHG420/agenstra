package modelapi

import (
	"bytes"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletionReviewDoesNotReconstructExecutionMessages(t *testing.T) {
	for _, mode := range []string{"json_object", "output_tools"} {
		t.Run(mode, func(t *testing.T) {
			id := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Write and independently read the record.",
				Observations:     []agentcontract.Observation{{CallRef: "write-1", Capability: "records.write", Status: "succeeded", Arguments: agentcontract.JSON{"id": 23}, FactID: &id}},
				Facts:            []agentcontract.FactView{{Fact: agentcontract.Fact{FactID: id, SourceCapability: "records.write", Value: agentcontract.JSON{"id": 23, "enabled": false}}}},
				CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "Written and read", FactIDs: []string{id}}}
			input, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
			payload, err := model.requestPayload(input, "Review the completion against the recorded evidence.")
			if err != nil {
				t.Fatal(err)
			}
			messages := payload["messages"].([]any)
			if mode == "output_tools" {
				assertReviewProposalInputPreserved(t, payload, input)
			} else if len(messages) != 2 {
				t.Fatal("review appended an execution decision and repeated result after the review request")
			}
			message := messages[1].(agentcontract.JSON)
			if message["role"] != "user" || (mode == "json_object" && message["content"] != string(input)) {
				t.Fatal("review lost its original ordered evidence or completion proposal")
			}
			after, err := agentcontract.CanonicalJSON(packet)
			if err != nil || !bytes.Equal(input, after) {
				t.Fatal("review request construction mutated the caller's evidence")
			}
			packet.CompletionReview = nil
			ordinary, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload, err = model.requestPayload(ordinary, "Continue execution.")
			if err != nil || len(payload["messages"].([]any)) != 4 {
				t.Fatal("ordinary execution lost its saved call and actual result", err)
			}
		})
	}
}
