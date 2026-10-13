package react

import (
	"context"
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestIncompleteCitedReadRequiresReviewBeforePublication(t *testing.T) {
	r, state := consistencyFixture(t)
	r.Provider.(*coreTestProvider).caps["record.create"] = agentcontract.CapabilityDescription{Name: "record.create", Effect: "read"}
	entries := agentcontract.JSON{}
	for i := range 80 {
		entries[fmt.Sprintf("item-%02d", i)] = strings.Repeat("e", 100)
	}
	state.Facts[0].Value = agentcontract.JSON{"data": entries}
	factID := state.Facts[0].FactID
	calls := 0
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		calls++
		if len(packet.ActionOutcomes) != 0 || len(packet.Facts[0].OmittedPaths) == 0 {
			t.Fatal("fixture must be a partial read with no write outcome", packet)
		}
		decision := agentcontract.Decision{Kind: "final", AnswerMarkdown: "All entries are complete.", FactIDs: []string{factID}}
		if calls == 2 {
			if packet.CompletionReview == nil || !strings.Contains(prompt, "omitted_paths") || state.AnswerMarkdown != "" {
				t.Fatal("partial read was not held for evidence review", packet)
			}
			decision.CompletionFeedback = "Inspect the omitted requested entries in the saved Fact."
		}
		return decision, nil
	})
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "running" || state.AnswerMarkdown != "" || calls != 2 || len(state.Pending) != 0 {
		t.Fatal("partial read answer was published or review dispatched work", state.Status, calls, state.Pending)
	}
	if state.ModelUsage.Requests != 2 || state.ModelCalls[1].Purpose != "completion_review" {
		t.Fatal("read evidence review escaped the original accounting", state.ModelCalls)
	}
	if r.Provider.(*coreTestProvider).called != 0 {
		t.Fatal("review executed a business operation")
	}
}

func TestUncitedPartialReadDoesNotReviewCompleteEvidence(t *testing.T) {
	r, state := consistencyFixture(t)
	r.Provider.(*coreTestProvider).caps["record.create"] = agentcontract.CapabilityDescription{Name: "record.create", Effect: "read"}
	completeID := state.Facts[0].FactID
	state.Facts = append(state.Facts, agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "record.create", Value: agentcontract.JSON{"data": strings.Repeat("x", 8000)}})
	r.Model = decisionModelFunc(func(_ context.Context, _ agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "The requested record is R-1.", FactIDs: []string{completeID}}, nil
	})
	if err := r.Step(t.Context(), state, nil); err != nil || state.Status != "completed" || state.ModelUsage.Requests != 1 {
		t.Fatal("unrelated partial read added a review", err, state.ModelCalls)
	}
}

func TestPartialReadReviewRespectsOriginalRoundBudget(t *testing.T) {
	r, state := consistencyFixture(t)
	r.Provider.(*coreTestProvider).caps["record.create"] = agentcontract.CapabilityDescription{Name: "record.create", Effect: "read"}
	state.Facts[0].Value = agentcontract.JSON{"data": strings.Repeat("x", 8000)}
	r.MaxModelRounds = 1
	r.Model = decisionModelFunc(func(_ context.Context, _ agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Everything is complete.", FactIDs: []string{state.Facts[0].FactID}}, nil
	})
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "failed" || state.AnswerMarkdown != "" || state.ModelUsage.Requests != 1 {
		t.Fatal("unreviewed partial evidence bypassed the round budget", state.Status, state.ModelUsage)
	}
}
