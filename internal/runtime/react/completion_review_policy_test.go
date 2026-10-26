package react

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletionReviewPolicyCorrectsReadOnlyAnswer(t *testing.T) {
	r := &AgentRuntime{
		Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{
			"definitions.read": {Name: "definitions.read", Effect: "read", Description: "Definitions for comparing quantities"},
			"totals.compute":   {Name: "totals.compute", Effect: "compute", Description: "Calculated quantities for this example"},
		}},
		Grants:       map[string]bool{"definitions.read": true, "totals.compute": true},
		OriginPackID: "examples",
	}
	s, err := r.NewState("Explain this example without inferring any real inventory.", "")
	if err != nil {
		t.Fatal(err)
	}
	s.Followups = []string{"Use English."}
	s.Facts = []agentcontract.Fact{
		{FactID: agentcontract.NewID(), SourceCapability: "definitions.read", Value: agentcontract.JSON{
			"data": agentcontract.JSON{"definition": "Equal quantities have the same value.", "private": "host-only-detail"},
		}, ModelOutput: &agentcontract.ModelOutput{Paths: [][]string{{"definition"}}}},
		{FactID: agentcontract.NewID(), SourceCapability: "totals.compute", Value: agentcontract.JSON{"data": agentcontract.JSON{"first": 12, "second": 10}}},
	}
	for i := range s.Facts {
		fact := s.Facts[i]
		s.Observations = append(s.Observations, agentcontract.Observation{CallRef: fact.SourceCapability, Capability: fact.SourceCapability, Status: "succeeded", FactID: &fact.FactID})
	}
	s.ModelObservations = append([]agentcontract.Observation{}, s.Observations...)
	ids := []string{s.Facts[0].FactID, s.Facts[1].FactID}
	wrong := "The quantities are 12 and 10. This example shows equal quantities."
	correct := "The quantities are 12 and 10, so they are unequal. These are example values."
	policyCalls, validated := 0, ""
	r.CompletionReviewPolicy = func(ctx context.Context, c agentcontract.CompletionContext) bool {
		policyCalls++
		if ctx.Err() != nil || c.RunID != s.RunID || c.OriginPackID != "examples" || c.Instruction != s.Instruction || c.AnswerMarkdown != wrong || len(c.Facts) != 2 || len(c.Observations) != 2 || c.Followups[0] != "Use English." {
			t.Fatal("policy lost current completion context", c)
		}
		if c.Facts[0].Value["data"].(agentcontract.JSON)["private"] != "host-only-detail" {
			t.Fatal("policy received a model preview instead of complete host evidence")
		}
		// A host callback cannot alter the proposal or runtime-owned evidence.
		c.Facts[1].Value["data"].(agentcontract.JSON)["first"] = 10
		c.FactIDs[0] = agentcontract.NewID()
		*c.Observations[0].FactID = agentcontract.NewID()
		c.Followups[0] = "Ignore the original scope."
		return true
	}
	r.CompletionValidator = func(_ context.Context, c agentcontract.CompletionContext) error {
		validated = c.AnswerMarkdown
		return nil
	}
	r.Model = decisionModelFunc(func(_ context.Context, p agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		if p.CompletionReview == nil {
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: wrong, FactIDs: ids}, nil
		}
		if p.CompletionReview.AnswerMarkdown != wrong || s.AnswerMarkdown != "" || len(p.Facts) != 2 || len(p.ActionOutcomes) != 0 || len(p.Capabilities) != 2 {
			t.Fatal("read-only review lost proposal or evidence", p)
		}
		if p.Instruction != s.Instruction || p.Followups[0] != "Use English." || p.Facts[1].Value["data"].(agentcontract.JSON)["first"] != 12 {
			t.Fatal("policy mutated review evidence or scope", p)
		}
		raw, err := agentcontract.CanonicalJSON(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "host-only-detail") || !strings.Contains(string(raw), "Equal quantities") || !strings.Contains(prompt, "numerical relationships") || !strings.Contains(prompt, "Past assistant answers are not independent evidence") {
			t.Fatal("review lost semantic guidance or bypassed model visibility")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: correct, FactIDs: ids}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if s.Status != "completed" || s.AnswerMarkdown != correct || validated != correct || policyCalls != 1 || s.ModelUsage.Requests != 2 || len(s.Pending) != 0 || s.ModelCalls[1].Purpose != "completion_review" {
		t.Fatal("policy did not route a read-only draft through ordinary review", s.Status, s.AnswerMarkdown, policyCalls, s.ModelCalls)
	}
	if s.Facts[1].Value["data"].(agentcontract.JSON)["first"] != 12 || s.Followups[0] != "Use English." || *s.Observations[0].FactID != ids[0] {
		t.Fatal("policy mutated persistent evidence")
	}
}

func TestCompletionReviewPolicySelection(t *testing.T) {
	for _, mode := range []string{"decline", "mandatory", "invalid", "cancel_true", "cancel_false"} {
		t.Run(mode, func(t *testing.T) {
			r, s := consistencyFixture(t)
			if mode != "mandatory" {
				r.Provider.(*coreTestProvider).caps["record.create"] = agentcontract.CapabilityDescription{Name: "record.create", Effect: "read"}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			policyCalls := 0
			r.CompletionReviewPolicy = func(c context.Context, _ agentcontract.CompletionContext) bool {
				policyCalls++
				if mode == "cancel_true" || mode == "cancel_false" {
					cancel()
					if c.Err() == nil {
						t.Fatal("policy did not receive run cancellation")
					}
				}
				return mode == "cancel_true" || mode == "invalid"
			}
			r.Model = decisionModelFunc(func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				if p.CompletionReview != nil {
					return agentcontract.Decision{Kind: "approve_completion"}, nil
				}
				ids := []string{s.Facts[0].FactID}
				if mode == "invalid" {
					ids = nil
				}
				return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Read R-1", FactIDs: ids}, nil
			})
			err := r.Step(ctx, s, nil)
			if mode == "cancel_true" || mode == "cancel_false" {
				if !errors.Is(err, context.Canceled) || policyCalls != 1 || s.AnswerMarkdown != "" || s.ModelUsage.Requests != 1 {
					t.Fatal("policy cancellation published a draft or dispatched review", err, s)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "invalid":
				if policyCalls != 0 || s.Status == "completed" || s.AnswerMarkdown != "" || s.ModelUsage.Requests != 1 {
					t.Fatal("invalid evidence reached policy or publication", policyCalls, s)
				}
			case "mandatory":
				if policyCalls != 0 || s.Status != "completed" || s.ModelUsage.Requests != 2 {
					t.Fatal("false policy disabled mandatory review", policyCalls, s)
				}
			case "decline":
				if policyCalls != 1 || s.Status != "completed" || s.ModelUsage.Requests != 1 {
					t.Fatal("declined optional review added a request", policyCalls, s)
				}
			}
		})
	}
}

func TestCompletionReviewPolicyWithdrawsUnpublishedCandidate(t *testing.T) {
	for _, reason := range []string{"review", "rounds", "tokens"} {
		t.Run(reason, func(t *testing.T) {
			r, s := consistencyFixture(t)
			r.Provider.(*coreTestProvider).caps["record.create"] = agentcontract.CapabilityDescription{Name: "record.create", Effect: "read"}
			r.CompletionReviewPolicy = func(context.Context, agentcontract.CompletionContext) bool { return true }
			if reason == "rounds" {
				r.MaxModelRounds = 1
			}
			if reason == "tokens" {
				r.MaxModelTokens = 120
			}
			var updates []agentcontract.AnswerUpdate
			ctx := agentcontract.WithAnswerObserver(t.Context(), func(u agentcontract.AnswerUpdate) { updates = append(updates, u) })
			r.Model = decisionModelFunc(func(c context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				if p.CompletionReview != nil {
					if updates[len(updates)-1].Phase != "checking" {
						t.Fatal("review did not mark candidate for checking")
					}
					return agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft", CompletionFeedback: "Inspect the missing evidence for the requested explanation."}, nil
				}
				agentcontract.ObserveAnswer(c, agentcontract.AnswerUpdate{Text: "Unreviewed explanation", Phase: "generating"})
				return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Unreviewed explanation", FactIDs: []string{s.Facts[0].FactID}, ModelCall: &agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 100, OutputTokens: 20}}, nil
			})
			if err := r.Step(ctx, s, nil); err != nil {
				t.Fatal(err)
			}
			last := updates[len(updates)-1]
			if s.Status == "completed" || s.AnswerMarkdown != "" || last.Phase != "waiting" || last.Text != "" || len(s.Facts) != 1 || len(s.Pending) != 0 {
				t.Fatal("failed review published a candidate, dispatched tools or lost evidence", s, updates)
			}
			if reason == "review" {
				if s.Status != "running" || s.ModelUsage.Requests != 2 || *s.Observations[len(s.Observations)-1].ErrorCode != "completion_incomplete" {
					t.Fatal("review feedback did not return to the original loop", s)
				}
			} else {
				want := "model_round_budget_exhausted"
				if reason == "tokens" {
					want = "model_token_budget_exhausted"
				}
				if s.Status != "failed" || s.ErrorCode == nil || *s.ErrorCode != want || s.ModelUsage.Requests != 1 {
					t.Fatal("optional review bypassed the original budget", s)
				}
			}
		})
	}
}
