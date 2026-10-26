package react

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCandidateWithdrawsOnReviewAndHostRejection(t *testing.T) {
	for _, rejection := range []string{"review", "host", "evidence"} {
		t.Run(rejection, func(t *testing.T) {
			r, s := consistencyFixture(t)
			var updates []agentcontract.AnswerUpdate
			ctx := agentcontract.WithAnswerObserver(t.Context(), func(u agentcontract.AnswerUpdate) { updates = append(updates, u) })
			r.Model = decisionModelFunc(func(ctx context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				if p.CompletionReview != nil {
					if len(updates) == 0 || updates[len(updates)-1].Phase != "checking" {
						t.Fatal("review did not retain checking proposal")
					}
					if rejection == "review" {
						return agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft", CompletionFeedback: "Missing work"}, nil
					}
					return agentcontract.Decision{Kind: "approve_completion"}, nil
				}
				agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: "Created R-1", Phase: "generating"})
				d := agentcontract.Decision{Kind: "final", AnswerMarkdown: "Created R-1", FactIDs: []string{s.Facts[0].FactID}}
				if rejection == "evidence" {
					d.FactIDs = nil
				}
				return d, nil
			})
			if rejection == "host" {
				r.CompletionValidator = func(context.Context, agentcontract.CompletionContext) error { return errors.New("reject") }
			}
			if err := r.Step(ctx, s, nil); err != nil {
				t.Fatal(err)
			}
			last := updates[len(updates)-1]
			if last.Phase != "waiting" || last.Text != "" || s.Status == "completed" {
				t.Fatal("rejected candidate retained", updates, s.Status)
			}
		})
	}
}

func TestCandidateCannotLeakSplitKnownFactID(t *testing.T) {
	r, s := consistencyFixture(t)
	id := strings.ToUpper(s.Facts[0].FactID)
	var updates []agentcontract.AnswerUpdate
	ctx := agentcontract.WithAnswerObserver(t.Context(), func(u agentcontract.AnswerUpdate) { updates = append(updates, u) })
	r.Model = decisionModelFunc(func(ctx context.Context, _ agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		for n := 1; n <= len(id); n++ {
			agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: "Evidence: " + id[:n], Phase: "generating"})
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Evidence: " + id, FactIDs: []string{s.Facts[0].FactID}}, nil
	})
	if err := r.Step(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	for _, update := range updates {
		if update.Text != "" && update.Text != "Evidence: " {
			t.Fatal("internal identifier leaked in a chunk", update)
		}
	}
	if s.Status == "completed" {
		t.Fatal("internal identifier published")
	}
	business := agentcontract.NewID()
	filtered := answerContext(ctx, s)
	agentcontract.ObserveAnswer(filtered, agentcontract.AnswerUpdate{Text: business, Phase: "checking"})
	if updates[len(updates)-1].Text != business {
		t.Fatal("ordinary business identifier lost")
	}
}
