package react

import (
	"bytes"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRemainingRunBudgetProjectsDecisionAndReviewInput(t *testing.T) {
	for _, review := range []bool{false, true} {
		t.Run(map[bool]string{false: "decision", true: "completion review"}[review], func(t *testing.T) {
			runtime, state, _ := contextBudgetRuntime(t, 20000)
			runtime.MaxModelInputTokens = 10000
			runtime.MaxModelOutputTokens = 500
			runtime.MaxModelTokens = 10000
			state.ModelUsage.BudgetTokens = 6000
			for range 6 {
				state.Facts = append(state.Facts, contextBudgetFact(agentcontract.JSON{"body": strings.Repeat("x", 1000)}))
			}
			packet := runtime.Context(state)
			if review {
				packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: "Verified summary", FactIDs: []string{state.Facts[5].FactID}}
			}
			before, valueErr1 := agentcontract.CanonicalJSON(state)
			if valueErr1 != nil {
				t.Fatal(valueErr1)
			}
			projected, _, limit, reserve, measurement, err := runtime.tokenProjection(state, packet, "")
			if err != nil || limit == nil || *limit != 3500 || reserve == nil || *reserve != 500 || measurement.Tokens > *limit || !measurement.TargetMet {
				t.Fatalf("remaining budget did not bound the final input: limit=%v reserve=%v measurement=%+v error=%v", limit, reserve, measurement, err)
			}
			if projected.MaxModelOutputTokens != 500 || projected.ModelTokensRemaining != 4000 || len(projected.Facts) != 6 || projected.Facts[5].FactID != state.Facts[5].FactID || (projected.CompletionReview != nil) != review {
				t.Fatal("budget projection lost evidence identity, review state or configured output")
			}
			after, valueErr2 := agentcontract.CanonicalJSON(state)
			if valueErr2 != nil {
				t.Fatal(valueErr2)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("budget projection modified the persisted checkpoint")
			}
		})
	}
}

func TestRemainingRunBudgetReportsRequiredInputExhaustion(t *testing.T) {
	runtime, state, _ := contextBudgetRuntime(t, 20000)
	runtime.MaxModelInputTokens = 10000
	runtime.MaxModelOutputTokens = 500
	state.Instruction = strings.Repeat("required", 1000)
	packet := runtime.Context(state)
	packet.ModelTokensRemaining = 4000
	_, _, limit, _, measurement, err := runtime.tokenProjection(state, packet, "")
	if agentcontract.ModelErrorCode(err) != "model_token_budget_exhausted" || limit == nil || measurement.Tokens <= *limit {
		t.Fatal("required input exhaustion was mislabeled as a model context limit", measurement, err)
	}
}

func TestUnknownModelWindowIsNotReportedAsZero(t *testing.T) {
	r, state, _ := contextBudgetRuntime(t, 9000)
	r.Model = &coreTestModel{decisions: []agentcontract.Decision{{Kind: "request_input", Field: "x", Prompt: "x"}}}
	if callErr6 := r.Step(t.Context(), state, nil); callErr6 != nil {
		t.Error(callErr6)
	}
	c := state.ContextTelemetry
	if c.ModelContextWindowTokens != nil || c.EffectiveInputTokenLimit != nil || c.TokensRemaining != nil || c.InputTokens == nil {
		t.Fatalf("%+v", c)
	}
}
