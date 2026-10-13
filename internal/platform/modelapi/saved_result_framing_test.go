package modelapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRecordedResultIdentifiesItsRunWithoutInventingFreshness(t *testing.T) {
	for _, mode := range []string{"json_object", "output_tools"} {
		for _, status := range []string{"succeeded", "accepted", "failed", "unknown"} {
			t.Run(mode+"/"+status, func(t *testing.T) {
				last := agentcontract.Observation{CallRef: "read-1", Capability: "records.read", Arguments: agentcontract.JSON{}, Status: status}
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Read once, then continue", Followups: []string{"record_id: 23"}, Observations: []agentcontract.Observation{last}}
				before, err := agentcontract.CanonicalJSON(packet)
				if err != nil {
					t.Fatal(err)
				}
				model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
				payload, err := model.requestPayload(before, "Choose the next decision")
				if err != nil {
					t.Fatal(err)
				}
				messages := payload["messages"].([]any)
				message := messages[len(messages)-1].(agentcontract.JSON)
				content := message["content"].(string)
				prefix, raw, ok := strings.Cut(content, "\n")
				if !ok {
					t.Fatal("recorded outcome is missing")
				}
				if !strings.Contains(prefix, "already attempted in this run") || strings.Contains(prefix, "use the saved ordering note") {
					t.Fatal("current-run evidence was framed as unspecified prior history or referenced missing ordering data")
				}
				if !strings.Contains(prefix, "Message placement alone does not establish timing relative to supplied input") || !strings.Contains(prefix, "not a new user task or authorization") {
					t.Fatal("result presentation invented freshness or authorization")
				}
				var outcome struct {
					Observation agentcontract.Observation `json:"observation"`
					Fact        *agentcontract.FactView   `json:"fact"`
				}
				if err := json.Unmarshal([]byte(raw), &outcome); err != nil {
					t.Fatal(err)
				}
				if outcome.Observation.Status != status || outcome.Observation.CallRef != last.CallRef || outcome.Fact != nil {
					t.Fatal("framing changed the attempted call's outcome or fabricated evidence")
				}
				after, err := agentcontract.CanonicalJSON(packet)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("message framing mutated caller evidence")
				}
			})
		}
	}
}
