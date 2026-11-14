package react

import (
	"bytes"
	"context"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type completionBudgetModel struct{ decisionModelFunc }

func (completionBudgetModel) MeasureInput(agentcontract.ContextPacket, string) (agentcontract.InputMeasurement, error) {
	return agentcontract.InputMeasurement{Tokens: 1000, Source: "tokenizer"}, nil
}

func TestRemainingBudgetReservesTheFinalAnswerAndReview(t *testing.T) {
	for _, attemptTool := range []bool{false, true} {
		t.Run(map[bool]string{false: "answer and review", true: "no new tool"}[attemptTool], func(t *testing.T) {
			runtime, state, provider := contextBudgetRuntime(t, 20000)
			runtime.MaxModelInputTokens, runtime.MaxModelOutputTokens, runtime.MaxModelTokens = 5000, 100, 3300
			runtime.CompletionReviewPolicy = func(context.Context, agentcontract.CompletionContext) bool { return true }
			state.RoundsUsed = 1
			RecordModelCall(state, agentcontract.ModelCallMetrics{Attempts: 1, Round: 1, UsageAvailable: true, InputTokens: 1000, OutputTokens: 100})
			calls := 0
			runtime.Model = completionBudgetModel{decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
				calls++
				metrics := &agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 1000, OutputTokens: 100}
				if packet.CompletionReview != nil {
					return agentcontract.Decision{Kind: "approve_completion", ModelCall: metrics}, nil
				}
				if !strings.Contains(prompt, "remaining token budget") {
					t.Fatal("remaining allowance did not reserve a closing reply")
				}
				if attemptTool {
					decision := callDecision("records.get")
					decision.ModelCall = metrics
					return decision, nil
				}
				return agentcontract.Decision{Kind: "final", AnswerMarkdown: "The requested information remains unverified within this run.", ModelCall: metrics}, nil
			})}
			if err := runtime.Step(t.Context(), state, nil); err != nil || provider.called != 0 || len(state.Pending) != 0 {
				t.Fatal("closing decision dispatched more work", err, provider.called)
			}
			if attemptTool {
				if state.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "model_token_budget_exhausted" || calls != 1 {
					t.Fatal("closing restriction lost its budget failure", state.Status, calls)
				}
			} else if state.Status != "completed" || calls != 2 || state.ModelUsage.BudgetTokens > runtime.MaxModelTokens {
				t.Fatal("answer or required review exceeded the allowance", state.Status, calls, state.ModelUsage)
			}
		})
	}
}

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
