package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type decisionModelFunc func(context.Context, ContextPacket, string) (Decision, error)

func (f decisionModelFunc) Decide(ctx context.Context, packet ContextPacket, prompt string) (Decision, error) {
	return f(ctx, packet, prompt)
}

func TestCompletionRejectsUnsupportedAnswerThenAllowsCorrection(t *testing.T) {
	p := &coreTestProvider{caps: map[string]CapabilityDescription{}}
	answers := []string{"count=999", "count=1"}
	m := decisionModelFunc(func(ctx context.Context, packet ContextPacket, prompt string) (Decision, error) {
		answer := answers[0]
		answers = answers[1:]
		return Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: []string{packet.Facts[0].FactID}}, nil
	})
	validator, err := RequireFactValues(FactRequirement{Capability: "records.get", Path: []any{"data", "count"}, Value: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := &AgentRuntime{Provider: p, Model: m, CompletionValidator: func(ctx context.Context, result CompletionContext) error {
		if err := validator(ctx, result); err != nil {
			return err
		}
		if result.AnswerMarkdown != "count=1" {
			return CompletionValidationError{"answer_value_mismatch", "Report the count stored in the cited Fact."}
		}
		return nil
	}}
	s, callErr := r.NewState("Report count", "")
	if callErr != nil {
		t.Error(callErr)
	}
	s.Facts = []Fact{{FactID: NewID(), SourceCapability: "records.get", Value: JSON{"data": JSON{"count": 1}}}}
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

func TestCompletionRequirementsUseLatestCitedFullFact(t *testing.T) {
	validator, err := RequireFactValues(FactRequirement{Capability: "job.status", Path: []any{"data", "status"}, Value: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	old := Fact{FactID: NewID(), SourceCapability: "job.status", Value: JSON{"data": JSON{"status": "succeeded"}}}
	latest := Fact{FactID: NewID(), SourceCapability: "job.status", Value: JSON{"data": JSON{"status": "running"}}}
	result := CompletionContext{Facts: []Fact{old, latest}, FactIDs: []string{old.FactID}}
	if ErrorCode(validator(t.Context(), result)) != "completion_evidence_missing" {
		t.Fatal("old citation accepted")
	}
	result.FactIDs = append(result.FactIDs, latest.FactID)
	if ErrorCode(validator(t.Context(), result)) != "completion_evidence_mismatch" {
		t.Fatal("running job accepted")
	}
	latest.Value["data"].(JSON)["status"] = "succeeded"
	if err = validator(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	result.Observations = []Observation{{CallRef: "submit", FactID: &latest.FactID}, {CallRef: "submit", ErrorCode: strptr("operation_failed")}}
	if ErrorCode(validator(t.Context(), result)) != "completion_operation_failed" {
		t.Fatal("failed operation accepted")
	}
}

func TestHostCompletionValidatorUsesCompleteEvidence(t *testing.T) {
	p := &hostProvider{}
	p.hook = func(context.Context, string, JSON, *InvocationContext) (CapabilityResult, error) {
		return CapabilityResult{Data: JSON{"id": "R-1", "detail": strings.Repeat("x", 9000)}}, nil
	}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision("records.get")}})
	h.CompletionValidator = func(ctx context.Context, result CompletionContext) error {
		if result.OriginPackID != "records" || len(result.Facts[0].Value["data"].(JSON)["detail"].(string)) != 9000 {
			t.Fatal("validator received a preview")
		}
		return nil
	}
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestCompletionInternalErrorsDoNotLeak(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: &coreTestModel{decisions: []Decision{{Kind: "final", AnswerMarkdown: "hello"}}}, CompletionValidator: func(context.Context, CompletionContext) error { return errors.New("private connection details") }}
	s, callErr2 := r.NewState("hello", "")
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	raw, callErr3 := CanonicalJSON(s.ModelObservations)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if strings.Contains(string(raw), "private") || s.Status == "completed" {
		t.Fatal(string(raw))
	}
}
