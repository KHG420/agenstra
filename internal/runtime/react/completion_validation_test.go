package react

import (
	"context"
	"reflect"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletionRejectsInvalidEvidenceBeforeAndAfterReview(t *testing.T) {
	cases := []struct {
		name     string
		code     string
		bad      func(*agentcontract.Decision, string)
		feedback []string
	}{
		{"missing_citations", "final_fact_citations_invalid", func(d *agentcontract.Decision, _ string) { d.FactIDs = nil }, nil},
		{"unknown_citation", "final_fact_citations_invalid", func(d *agentcontract.Decision, _ string) { d.FactIDs = []string{agentcontract.NewID()} }, nil},
		{"array_reference", "final_result_refs_invalid", func(d *agentcontract.Decision, id string) {
			d.ResultRefs = []agentcontract.ResultRefRequest{{FactID: id, Path: []any{"data", "ip_whitelist"}}}
		}, nil},
		{"internal_fact_id", "final_internal_fact_id_exposed", func(d *agentcontract.Decision, id string) { d.AnswerMarkdown = "Created: " + strings.ToUpper(id) }, nil},
		{"boolean_reference_and_internal_id", "final_result_refs_invalid", func(d *agentcontract.Decision, id string) {
			d.ResultRefs = []agentcontract.ResultRefRequest{{FactID: id, Path: []any{"data", "schedulable"}}}
			d.AnswerMarkdown = "Created: " + id
		}, []string{"Each result_refs entry", "Remove internal Fact identifiers"}},
		{"all_evidence_errors", "final_fact_citations_invalid", func(d *agentcontract.Decision, id string) {
			d.FactIDs = nil
			d.ResultRefs = []agentcontract.ResultRefRequest{{FactID: id, Path: []any{"data", "schedulable"}}}
			d.AnswerMarkdown = "Created: " + id
		}, []string{"Cite at least one observed Fact", "Each result_refs entry", "Remove internal Fact identifiers"}},
	}
	for _, tc := range cases {
		for _, stage := range []string{"proposal", "review"} {
			t.Run(tc.name+"/"+stage, func(t *testing.T) {
				r, s := consistencyFixture(t)
				id := s.Facts[0].FactID
				s.Facts[0].Value["data"].(agentcontract.JSON)["ip_whitelist"] = []any{"127.0.0.1"}
				s.Facts[0].Value["data"].(agentcontract.JSON)["schedulable"] = false
				facts := append([]agentcontract.Fact{}, s.Facts...)
				originalObservation := s.Observations[0]
				valid := agentcontract.Decision{Kind: "final", AnswerMarkdown: "Created R-1", FactIDs: []string{id}, ResultRefs: []agentcontract.ResultRefRequest{{FactID: id, Path: []any{"data", "id"}}}}
				invalid := valid
				tc.bad(&invalid, id)
				calls, checkpoints, hostChecks := 0, 0, 0
				firstCalls := 1
				if stage == "review" {
					firstCalls = 2
				}
				r.CompletionValidator = func(_ context.Context, c agentcontract.CompletionContext) error {
					hostChecks++
					if c.AnswerMarkdown != valid.AnswerMarkdown {
						t.Fatal("host received invalid final")
					}
					return nil
				}
				r.Model = decisionModelFunc(func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
					calls++
					if calls == firstCalls {
						return invalid, nil
					}
					if calls > firstCalls {
						if len(p.Observations) != 2 || p.Observations[1].ErrorCode == nil || *p.Observations[1].ErrorCode != tc.code {
							t.Fatal("correction lost deterministic validation feedback", p.Observations)
						}
						feedback, _ := p.Observations[1].Arguments["feedback"].(string)
						for _, want := range tc.feedback {
							if !strings.Contains(feedback, want) {
								t.Fatalf("correction lost an independent validation failure %q: %s", want, feedback)
							}
						}
					} else if p.CompletionReview != nil {
						t.Fatal("invalid proposal was sent for review")
					}
					if calls == firstCalls+1 && p.CompletionReview != nil {
						t.Fatal("correction did not return to ordinary decision")
					}
					if calls == firstCalls+2 && (p.CompletionReview == nil || p.CompletionReview.AnswerMarkdown != valid.AnswerMarkdown) {
						t.Fatal("corrected proposal bypassed completion review")
					}
					return valid, nil
				})
				checkpoint := func() error { checkpoints++; return nil }
				if err := r.Step(t.Context(), s, checkpoint); err != nil {
					t.Fatal(err)
				}
				if calls != firstCalls || checkpoints != firstCalls || hostChecks != 0 || s.Status != "running" || s.AnswerMarkdown != "" || len(s.ResultRefs) != 0 {
					t.Fatalf("invalid final processed: calls=%d checkpoints=%d host=%d status=%s answer=%q", calls, checkpoints, hostChecks, s.Status, s.AnswerMarkdown)
				}
				if len(s.Observations) != 2 || s.Observations[1].ErrorCode == nil || *s.Observations[1].ErrorCode != tc.code || len(s.Decisions) != firstCalls || s.ModelUsage.Requests != firstCalls {
					t.Fatal("rejection lost code, decision or accounting", s.Observations, s.Decisions, s.ModelUsage)
				}
				if err := r.Step(t.Context(), s, checkpoint); err != nil {
					t.Fatal(err)
				}
				if s.Status != "completed" || s.AnswerMarkdown != valid.AnswerMarkdown || calls != firstCalls+2 || checkpoints != calls || hostChecks != 1 || s.ModelUsage.Requests != calls || s.RoundsUsed != calls {
					t.Fatal("valid correction or budgets changed", s.Status, calls, hostChecks, s.ModelUsage, s.RoundsUsed)
				}
				if len(s.ResultRefs) != 1 || s.ResultRefs[0].ID != "R-1" || s.ModelCalls[len(s.ModelCalls)-1].Purpose != "completion_review" || len(s.Pending) != 0 || r.Provider.(*coreTestProvider).called != 0 || s.ToolCallsUsed != 0 {
					t.Fatal("correction bypassed review or replayed business action", s.ResultRefs, s.ModelCalls, s.Pending)
				}
				if !reflect.DeepEqual(s.Facts, facts) || !reflect.DeepEqual(s.Observations[0], originalObservation) {
					t.Fatal("validation changed successful business evidence")
				}
			})
		}
	}
}

func TestInvalidCompletionProposalKeepsRoundBudget(t *testing.T) {
	r, s := consistencyFixture(t)
	r.MaxModelRounds = 1
	calls := 0
	r.Model = decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
		calls++
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Created"}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(s.Observations) != 2 || s.Observations[1].ErrorCode == nil || *s.Observations[1].ErrorCode != "final_fact_citations_invalid" || s.Status != "running" {
		t.Fatal("invalid draft consumed review or lost rejection", calls, s.Observations, s.Status)
	}
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || s.Status != "failed" || s.ErrorCode == nil || *s.ErrorCode != "model_round_budget_exhausted" || s.AnswerMarkdown != "" || s.ModelUsage.Requests != 1 || len(s.Facts) != 1 || s.Observations[0].Status != "succeeded" {
		t.Fatal("validation bypassed budget or lost business outcome", calls, s.Status, s.ErrorCode, s.ModelUsage)
	}
}
