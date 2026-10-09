package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestModelFollowupOutcomePreservesFailureAndProjectedEvidence(t *testing.T) {
	model := &HTTPJSONDecisionModel{Model: "fake"}
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Followups: []string{"label: supplied"},
		Observations: []agentcontract.Observation{{CallRef: "read-1", Capability: "record.read", Status: "failed", ErrorCode: agentcontract.Strptr("host_request_rejected"), Arguments: agentcontract.JSON{"id": 23}, FactID: agentcontract.Strptr("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}},
		Facts:        []agentcontract.FactView{{Fact: agentcontract.Fact{FactID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Value: agentcontract.JSON{"visible": "projected only"}}, OmittedPaths: [][]any{{"hidden"}}}},
	}
	input, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := model.requestPayload(input, "Return JSON")
	if err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	content := messages[3].(agentcontract.JSON)["content"].(string)
	if !strings.Contains(content, `"status":"failed"`) || !strings.Contains(content, "host_request_rejected") || !strings.Contains(content, `"omitted_paths":[["hidden"]]`) {
		t.Fatal("the recent outcome hid a failure or an incomplete preview", content)
	}
	for _, change := range []func(){
		func() { packet.Schema = "agenstra.memory-extraction.v1" },
		func() { packet.Schema = "agenstra.context.v1"; packet.Followups = []string{"steering: read again"} },
		func() { packet.Followups = []string{"label: supplied"}; packet.Observations[0].CallRef = "" },
		func() {
			packet.Observations[0].CallRef = "read-1"
			packet.Observations[0].Capability = "agent.final"
		},
		func() {
			packet.Schema = "agenstra.memory-extraction.v1"
			packet.Observations[0].Capability = "record.read"
		},
	} {
		change()
		input, err = agentcontract.CanonicalJSON(packet)
		if err != nil {
			t.Fatal(err)
		}
		payload, err = model.requestPayload(input, "Return JSON")
		if err != nil || len(payload["messages"].([]any)) != 2 {
			t.Fatal("extra execution messages were added to an ineligible context", err)
		}
	}
}
