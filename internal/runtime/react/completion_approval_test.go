package react

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompactCompletionApprovalPublishesOnlyFrozenProposal(t *testing.T) {
	r, s := consistencyFixture(t)
	want := strings.Repeat("verified business value\n", 900)
	factID := s.Facts[0].FactID
	retainedIDs := []string{factID}
	retainedRefs := []agentcontract.ResultRefRequest{{FactID: factID, Path: []any{"data", "id"}}}
	validated, calls := "", 0
	r.CompletionValidator = func(_ context.Context, c agentcontract.CompletionContext) error {
		validated = c.AnswerMarkdown
		if !reflect.DeepEqual(c.FactIDs, []string{factID}) {
			t.Fatal("validator received replaced references")
		}
		return nil
	}
	r.Model = decisionModelFunc(func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		calls++
		if p.CompletionReview == nil {
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: want, FactIDs: retainedIDs, ResultRefs: retainedRefs}, nil
		}
		if p.CompletionReview.AnswerMarkdown != want || s.AnswerMarkdown != "" {
			t.Fatal("review lost unpublished proposal")
		}
		// The model owns a detached packet, never the frozen proposal.
		p.CompletionReview.AnswerMarkdown = "malicious replacement"
		p.CompletionReview.FactIDs[0] = agentcontract.NewID()
		p.CompletionReview.ResultRefs[0].Path[1] = "secret"
		retainedIDs[0] = agentcontract.NewID()
		retainedRefs[0].Path[1] = "retained-secret"
		return agentcontract.Decision{Kind: "approve_completion"}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if s.Status != "completed" || s.AnswerMarkdown != want || validated != want || len(s.ResultRefs) != 1 || s.ResultRefs[0].ID != "R-1" || calls != 2 || len(s.Pending) != 0 {
		t.Fatalf("verdict did not publish exact checked draft: status=%s calls=%d validated=%t refs=%v", s.Status, calls, validated == want, s.ResultRefs)
	}
	if len(s.Decisions) != 2 || s.Decisions[1]["kind"] != "approve_completion" || s.ModelCalls[1].Purpose != "completion_review" || s.ModelUsage.Requests != 2 {
		t.Fatal("verdict lost decision history or accounting", s.Decisions, s.ModelCalls)
	}
}

func TestCompactCompletionApprovalCannotBypassPublicationChecks(t *testing.T) {
	for _, failure := range []string{"ordinary", "payload", "validator", "citation", "internal ID", "rounds", "tokens", "cancel", "checkpoint", "missing work"} {
		t.Run(failure, func(t *testing.T) {
			r, s := consistencyFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			r.Model = decisionModelFunc(func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				calls++
				if failure == "ordinary" {
					return agentcontract.Decision{Kind: "approve_completion"}, nil
				}
				if p.CompletionReview == nil {
					answer, id := "Created R-1", s.Facts[0].FactID
					if failure == "citation" {
						id = agentcontract.NewID()
					}
					if failure == "internal ID" {
						answer += " " + id
					}
					return agentcontract.Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: []string{id}, ModelCall: &agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 100, OutputTokens: 20}}, nil
				}
				if failure == "cancel" {
					cancel()
				}
				if failure == "missing work" {
					return agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft", CompletionFeedback: "Missing requested independent read"}, nil
				}
				d := agentcontract.Decision{Kind: "approve_completion"}
				if failure == "payload" {
					d.AnswerMarkdown = "unchecked"
				}
				return d, nil
			})
			if failure == "validator" {
				r.CompletionValidator = func(context.Context, agentcontract.CompletionContext) error {
					return errors.New("reject business result")
				}
			}
			if failure == "rounds" {
				r.MaxModelRounds = 1
			}
			if failure == "tokens" {
				r.MaxModelTokens = 120
			}
			checkpoints := 0
			err := r.Step(ctx, s, func() error {
				checkpoints++
				if failure == "checkpoint" && checkpoints == 2 {
					return errors.New("checkpoint failed")
				}
				return nil
			})
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("review ignored cancellation", err)
			}
			if failure == "checkpoint" && err == nil {
				t.Fatal("ignored checkpoint failure")
			}
			if s.Status == "completed" || s.AnswerMarkdown != "" || len(s.Pending) != 0 {
				t.Fatal("unapproved result published or dispatched tools", failure, s.Status)
			}
			if failure == "missing work" && (s.Status != "running" || *s.ModelObservations[len(s.ModelObservations)-1].ErrorCode != "completion_incomplete") {
				t.Fatal("lost missing-work feedback")
			}
		})
	}
}

func TestCompactCompletionApprovalResumeReviewsFreshFrozenProposal(t *testing.T) {
	r, s := consistencyFixture(t)
	r.Model = &hostModel{}
	n := 0
	if err := r.Step(t.Context(), s, func() error {
		n++
		if n == 2 {
			return errors.New("lost lease before review")
		}
		return nil
	}); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var resumed agentcontract.RuntimeState
	if err := json.Unmarshal(raw, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Status == "completed" || resumed.AnswerMarkdown != "" {
		t.Fatal("checkpoint published unreviewed draft")
	}
	calls := 0
	r.Model = decisionModelFunc(func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		calls++
		if p.CompletionReview == nil {
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Fresh verified draft", FactIDs: []string{resumed.Facts[0].FactID}}, nil
		}
		if p.CompletionReview.AnswerMarkdown != "Fresh verified draft" {
			t.Fatal("review approved stale saved draft")
		}
		return agentcontract.Decision{Kind: "approve_completion"}, nil
	})
	if err := r.Step(t.Context(), &resumed, nil); err != nil || resumed.Status != "completed" || resumed.AnswerMarkdown != "Fresh verified draft" || calls != 2 {
		t.Fatal("resume lost ordinary review", err, resumed.Status, calls)
	}
}
