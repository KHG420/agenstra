package react

import (
	"bytes"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestContextBudgetRestoresFittingCallIntentAfterSchemaDeferral(t *testing.T) {
	for _, test := range []struct {
		name     string
		reason   string
		omitted  bool
		restored bool
	}{
		{name: "fitting", reason: "Read context before the next requested detail.", restored: true},
		{name: "oversized", reason: strings.Repeat("i", 4000)},
		{name: "hidden arguments", reason: "Do not recover hidden call arguments.", omitted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			packet := agentcontract.ContextPacket{
				Schema: "agenstra.context.v1", Instruction: "Read the remaining record",
				Capabilities: []agentcontract.JSON{{"name": "record.read", "input_schema": agentcontract.JSON{
					"type": "object", "properties": agentcontract.JSON{"query": agentcontract.JSON{"type": "string", "description": strings.Repeat("s", 12000)}},
				}}},
				LastCallReason: test.reason,
				Observations:   []agentcontract.Observation{{CallRef: "context-1", Capability: "record.read", Status: "succeeded", Arguments: agentcontract.JSON{}, ArgumentsOmitted: test.omitted}},
			}
			before, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			projected := budgetContext(packet, &agentcontract.RuntimeState{}, 2000)
			if contextCharacters(projected) > 2000 {
				t.Fatal("restoring intent exceeded the input budget")
			}
			if (projected.LastCallReason == test.reason) != test.restored || (!test.restored && projected.LastCallReason != "") {
				t.Fatal("call intent was lost despite freed space or recovered without a visible fitting call", projected.LastCallReason)
			}
			after, err := agentcontract.CanonicalJSON(packet)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("projection changed the caller's packet", err)
			}
		})
	}
}
