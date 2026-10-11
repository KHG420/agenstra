package react

import (
	"context"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletionReviewPreservesEvidenceWithoutExecutionCatalog(t *testing.T) {
	r, state := consistencyFixture(t)
	provider := r.Provider.(*coreTestProvider)
	capability := provider.caps["record.create"]
	capability.InputSchema = agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
		"value": agentcontract.JSON{"type": "string", "enum": []any{strings.Repeat("catalog-only-value-", 100)}},
	}}
	provider.caps[capability.Name] = capability
	provider.caps["record.read"] = agentcontract.CapabilityDescription{Name: "record.read", Effect: "read"}
	r.Grants = map[string]bool{"record.create": true, "record.read": true}
	r.MaxContextCapabilities = 1
	state.CapabilitySearchQuery = "record"
	state.CapabilitySearchResults = []string{"record.create", "record.read"}
	state.InspectedCapability = &capability.Name
	state.Followups = []string{"Preserve the requested read after the write."}
	requests := 0
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		requests++
		if packet.CompletionReview == nil {
			if len(packet.Capabilities) == 0 || len(packet.InspectedCapability) == 0 {
				t.Fatal("ordinary execution lost its authorized capability contracts")
			}
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Created", FactIDs: []string{state.Facts[0].FactID}}, nil
		}
		if len(packet.Capabilities) != 0 || len(packet.CapabilitySearchResults) != 0 || len(packet.InspectedCapability) != 0 {
			t.Fatal("final-only review still receives execution catalog schemas")
		}
		if len(packet.Facts) != 1 || packet.Facts[0].FactID != state.Facts[0].FactID || len(packet.Observations) != 1 || packet.Observations[0].CallRef != "create-1" || len(packet.ActionOutcomes) != 1 || packet.ActionOutcomes[0].Status != "succeeded" || len(packet.Followups) != 1 {
			t.Fatal("review projection lost original evidence or user constraints", packet)
		}
		if state.AnswerMarkdown != "" || len(state.Pending) != 0 {
			t.Fatal("review published a draft or dispatched work")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Created", FactIDs: []string{state.Facts[0].FactID}, CompletionFeedback: "The requested independent read is missing."}, nil
	})
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || state.Status != "running" || state.AnswerMarkdown != "" {
		t.Fatal("review did not return its missing work to execution", state)
	}
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if packet.CompletionReview != nil || len(packet.Capabilities) == 0 || len(packet.InspectedCapability) == 0 || len(packet.CapabilitySearchResults) == 0 {
			t.Fatal("review projection mutated the next ordinary decision's catalog", packet)
		}
		return callDecision("record.read"), nil
	})
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if len(state.Pending) != 1 || state.Pending[0].Call.Capability != "record.read" || len(state.Facts) != 1 {
		t.Fatal("ordinary decision did not prepare the missing read", state)
	}
}
