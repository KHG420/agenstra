package react

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type decisionModelFunc func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error)

func (f decisionModelFunc) Decide(ctx context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
	return f(ctx, packet, prompt)
}

func TestCompletionRejectsUnsupportedAnswerThenAllowsCorrection(t *testing.T) {
	p := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
	answers := []string{"count=999", "count=1"}
	m := decisionModelFunc(func(ctx context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		answer := answers[0]
		answers = answers[1:]
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: []string{packet.Facts[0].FactID}}, nil
	})
	validator, err := agentcontract.RequireFactValues(agentcontract.FactRequirement{Capability: "records.get", Path: []any{"data", "count"}, Value: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := &AgentRuntime{Provider: p, Model: m, CompletionValidator: func(ctx context.Context, result agentcontract.CompletionContext) error {
		if err := validator(ctx, result); err != nil {
			return err
		}
		if result.AnswerMarkdown != "count=1" {
			return agentcontract.CompletionValidationError{Kind: "answer_value_mismatch", Feedback: "Report the count stored in the cited Fact."}
		}
		return nil
	}}
	s, callErr := r.NewState("Report count", "")
	if callErr != nil {
		t.Error(callErr)
	}
	s.Facts = []agentcontract.Fact{{FactID: agentcontract.NewID(), SourceCapability: "records.get", Value: agentcontract.JSON{"data": agentcontract.JSON{"count": 1}}}}
	if err = r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if s.Status == "completed" || *s.Observations[0].ErrorCode != "answer_value_mismatch" {
		t.Fatalf("accepted unsupported answer: %+v", s)
	}
	if err = r.Step(t.Context(), s, nil); err != nil || s.Status != "completed" || s.AnswerMarkdown != "count=1" {
		t.Fatalf("correction: %+v %v", s, err)
	}
}

func TestCompletionInternalErrorsDoNotLeak(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: &coreTestModel{decisions: []agentcontract.Decision{{Kind: "final", AnswerMarkdown: "hello"}}}, CompletionValidator: func(context.Context, agentcontract.CompletionContext) error {
		return errors.New("private connection details")
	}}
	s, callErr2 := r.NewState("hello", "")
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	raw, callErr3 := agentcontract.CanonicalJSON(s.ModelObservations)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if strings.Contains(string(raw), "private") || s.Status == "completed" {
		t.Fatal(string(raw))
	}
}
