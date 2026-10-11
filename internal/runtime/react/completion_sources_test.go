package react

import (
	"context"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletionReviewRetainsObservedSourceSemanticsWithoutInvocationContracts(t *testing.T) {
	r, state := consistencyFixture(t)
	provider := r.Provider.(*coreTestProvider)
	cap := provider.caps["record.create"]
	cap.Description = "The business value is in the returned data field; the wrapper describes delivery only."
	cap.InputSchema = agentcontract.JSON{"type": "object", "description": strings.Repeat("private-execution-catalog-", 1000)}
	provider.caps[cap.Name] = cap
	provider.caps["record.unused"] = agentcontract.CapabilityDescription{Name: "record.unused", Description: "unused-contract-marker"}
	provider.caps["record.revoked"] = agentcontract.CapabilityDescription{Name: "record.revoked", Description: "revoked-contract-marker"}
	r.Grants = map[string]bool{cap.Name: true, "record.unused": true}
	// The terminal Fact belongs to the poll capability, while the observation
	// retains the original action's source semantics.
	state.Facts[0].SourceCapability = "record.poll"
	state.Facts = append(state.Facts, agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "record.revoked", Value: agentcontract.JSON{"data": nil}})
	state.CapabilitySearchResults = []string{"record.create", "record.unused"}
	requests := 0
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		requests++
		if packet.CompletionReview == nil {
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Created", FactIDs: []string{state.Facts[0].FactID}}, nil
		}
		if len(packet.Capabilities) != 1 || packet.Capabilities[0]["name"] != cap.Name || packet.Capabilities[0]["description"] != cap.Description || len(packet.Capabilities[0]) != 2 {
			t.Fatal("review lost the original result semantics or retained an execution contract", packet.Capabilities)
		}
		if len(packet.CapabilitySearchResults) != 0 || len(packet.InspectedCapability) != 0 {
			t.Fatal("review regained executable discovery", packet)
		}
		raw, err := agentcontract.CanonicalJSON(packet)
		if err != nil || strings.Contains(string(raw), "private-execution-catalog-") || strings.Contains(string(raw), "unused-contract-marker") || strings.Contains(string(raw), "revoked-contract-marker") {
			t.Fatal("unobserved, revoked or executable metadata entered review", err)
		}
		if state.AnswerMarkdown != "" || len(state.Pending) != 0 {
			t.Fatal("source descriptions changed execution authority")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Created", FactIDs: []string{state.Facts[0].FactID}}, nil
	})
	if err := r.Step(t.Context(), state, nil); err != nil || state.Status != "completed" || requests != 2 || provider.called != 0 || state.ToolCallsUsed != 0 {
		t.Fatal("description-only review dispatched work or failed", state.Status, requests, provider.called, err)
	}
}
