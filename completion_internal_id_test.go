package agenstra

import (
	"context"
	"strings"
	"testing"
)

func TestFinalAnswerInternalFactIDGuard(t *testing.T) {
	for _, variant := range []string{"plain", "uppercase", "code", "url"} {
		t.Run(variant, func(t *testing.T) {
			id := NewID()
			bad := id
			switch variant {
			case "uppercase":
				bad = strings.ToUpper(id)
			case "code":
				bad = "`" + id + "`"
			case "url":
				bad = "https://example.test/facts/" + id
			}
			p := &coreTestProvider{caps: map[string]CapabilityDescription{}}
			calls := 0
			hostChecks := 0
			r := &AgentRuntime{Provider: p, CompletionValidator: func(context.Context, CompletionContext) error { hostChecks++; return nil }}
			s, e := r.NewState("Report business resource ID", "")
			if e != nil {
				t.Fatal(e)
			}
			s.Facts = []Fact{{FactID: id, Value: JSON{"data": JSON{"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}}}}
			r.Model = decisionModelFunc(func(_ context.Context, packet ContextPacket, _ string) (Decision, error) {
				calls++
				answer := "id=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
				if calls == 1 {
					answer = "Internal Fact: " + bad
				} else if len(packet.Observations) != 1 || packet.Observations[0].ErrorCode == nil || *packet.Observations[0].ErrorCode != "final_internal_fact_id_exposed" {
					t.Fatal("missing correction feedback")
				}
				return Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: []string{id}}, nil
			})
			if e = r.Step(t.Context(), s, nil); e != nil {
				t.Fatal(e)
			}
			if s.Status == "completed" || s.AnswerMarkdown != "" || hostChecks != 0 || len(s.Observations) != 1 || s.Observations[0].ErrorCode == nil || *s.Observations[0].ErrorCode != "final_internal_fact_id_exposed" {
				t.Fatal("leaking answer accepted or host validator bypassed guard")
			}
			if e = r.Step(t.Context(), s, nil); e != nil {
				t.Fatal(e)
			}
			if s.Status != "completed" || s.AnswerMarkdown != "id=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || hostChecks != 1 || p.called != 0 || s.ToolCallsUsed != 0 || s.RoundsUsed != 2 {
				t.Fatal("correction changed business ID, re-executed tools or skipped budgets")
			}
		})
	}
	t.Run("no_fact_business_uuid", func(t *testing.T) {
		answer := "id=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: &coreTestModel{decisions: []Decision{{Kind: "final", AnswerMarkdown: answer}}}}
		s, e := r.NewState("Report supplied business UUID", "")
		if e != nil {
			t.Fatal(e)
		}
		if e = r.Step(t.Context(), s, nil); e != nil || s.Status != "completed" || s.AnswerMarkdown != answer {
			t.Fatal("arbitrary UUID rejected")
		}
	})
	t.Run("original_citation_validation_priority", func(t *testing.T) {
		id := NewID()
		r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: &coreTestModel{decisions: []Decision{{Kind: "final", AnswerMarkdown: id}}}}
		s, e := r.NewState("Report", "")
		if e != nil {
			t.Fatal(e)
		}
		s.Facts = []Fact{{FactID: id, Value: JSON{"value": 1}}}
		if e = r.Step(t.Context(), s, nil); e != nil || s.Status == "completed" || *s.Observations[0].ErrorCode != "final_fact_citations_invalid" {
			t.Fatal("original citation validation changed")
		}
	})
}
