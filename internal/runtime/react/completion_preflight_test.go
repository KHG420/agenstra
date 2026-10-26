package react

import (
	"context"
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletionPreflightRejectsBeforeReviewAndPreservesEvidence(t *testing.T) {
	r, s := consistencyFixture(t)
	id := s.Facts[0].FactID
	preflights, reviews, validations := 0, 0, 0
	r.CompletionPreflight = func(_ context.Context, c agentcontract.CompletionContext) error {
		preflights++
		if len(c.Facts) != 1 || c.Facts[0].Value["data"].(agentcontract.JSON)["id"] != "R-1" {
			t.Fatal("preflight lost full retained evidence")
		}
		c.Facts[0].Value["data"].(agentcontract.JSON)["id"] = "changed"
		c.FactIDs[0] = "changed"
		if c.AnswerMarkdown == "invalid draft" {
			return agentcontract.CompletionValidationError{Kind: "answer_invalid", Feedback: "Correct the draft without repeating the write."}
		}
		return nil
	}
	r.CompletionValidator = func(_ context.Context, c agentcontract.CompletionContext) error {
		validations++
		if c.AnswerMarkdown != "Created R-1" || c.FactIDs[0] != id {
			t.Fatal("final check did not receive the reviewed answer")
		}
		return nil
	}
	r.Model = decisionModelFunc(func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if p.CompletionReview != nil {
			reviews++
			if p.CompletionReview.AnswerMarkdown != "Created R-1" {
				t.Fatal("invalid draft consumed a model review")
			}
			return agentcontract.Decision{Kind: "approve_completion"}, nil
		}
		answer := "invalid draft"
		if preflights > 0 {
			answer = "Created R-1"
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: []string{id}}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if s.Status == "completed" || reviews != 0 || validations != 0 || len(s.Pending) != 0 || s.ModelUsage.Requests != 1 {
		t.Fatal("preflight rejection published or dispatched work", s.Status, reviews, validations)
	}
	if s.Facts[0].Value["data"].(agentcontract.JSON)["id"] != "R-1" || *s.Observations[0].FactID != id {
		t.Fatal("preflight mutated runtime evidence")
	}
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if s.Status != "completed" || s.AnswerMarkdown != "Created R-1" || preflights != 2 || reviews != 1 || validations != 1 || s.ModelUsage.Requests != 3 || len(s.Pending) != 0 {
		t.Fatal("correction bypassed checks or replayed the write", s.Status, preflights, reviews, validations)
	}
}

func TestCompletionPreflightCancellationDoesNotDispatchReview(t *testing.T) {
	r, s := consistencyFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.CompletionPreflight = func(context.Context, agentcontract.CompletionContext) error { cancel(); return nil }
	r.Model = decisionModelFunc(func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if p.CompletionReview != nil {
			t.Fatal("cancelled preflight dispatched model review")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Created R-1", FactIDs: []string{s.Facts[0].FactID}}, nil
	})
	if err := r.Step(ctx, s, nil); !errors.Is(err, context.Canceled) || s.AnswerMarkdown != "" || s.ModelUsage.Requests != 1 {
		t.Fatal("preflight ignored cancellation", err, s.Status)
	}
}
