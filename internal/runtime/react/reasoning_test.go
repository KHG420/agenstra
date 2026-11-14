package react

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type reasoningTestModel struct{ decisionModelFunc }

func (reasoningTestModel) SupportsReasoningRequests() bool { return true }

func reasoningDecision(kind string) agentcontract.Decision {
	d := agentcontract.Decision{Kind: kind, ModelCall: &agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 50, OutputTokens: 20}}
	if kind == "final" {
		d.AnswerMarkdown = "done"
	}
	return d
}

func TestReasoningUpgradeSurvivesCheckpointWithoutExtraClassifier(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "routine", true: "complex"}[upgrade], func(t *testing.T) {
			calls := 0
			model := reasoningTestModel{func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				calls++
				if p.ReasoningRequested != (upgrade && calls > 1) || slices.Contains(p.RuntimeFeatures, "reasoning_request") != (calls == 1) {
					t.Fatal("mode or one-way availability lost", p.ReasoningRequested, p.RuntimeFeatures)
				}
				kind := "final"
				if upgrade && calls == 1 {
					kind = "request_reasoning"
				}
				return reasoningDecision(kind), nil
			}}
			runtime := &AgentRuntime{Provider: &coreTestProvider{}, Model: model}
			state, err := runtime.NewState("task", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			if upgrade {
				if !state.ReasoningRequested || state.Status != "running" || len(state.Pending) != 0 || state.ToolCallsUsed != 0 || state.AnswerMarkdown != "" {
					t.Fatal("upgrade executed business work or failed to persist", state)
				}
				raw, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				var restored agentcontract.RuntimeState
				if err := json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				state = &restored
				runtime = &AgentRuntime{Provider: &coreTestProvider{}, Model: model}
				if err := runtime.Step(t.Context(), state, nil); err != nil {
					t.Fatal(err)
				}
			}
			want := 1
			if upgrade {
				want = 2
			}
			if state.Status != "completed" || calls != want || state.RoundsUsed != want || len(state.ModelCalls) != want || state.ModelUsage.BudgetTokens != int64(want*70) || len(state.Observations) != 0 {
				t.Fatal("routing changed calls, usage or execution", calls, state)
			}
		})
	}
}

func TestReasoningRequestUnavailableUsesBoundedRecovery(t *testing.T) {
	for _, scenario := range []string{"unsupported model", "repeated", "last round", "completion review"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			base := decisionModelFunc(func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				calls++
				if p.CompletionReview != nil && slices.Contains(p.RuntimeFeatures, "reasoning_request") {
					t.Fatal("review offers reasoning control")
				}
				kind := "request_reasoning"
				if scenario == "completion review" && calls == 1 {
					kind = "final"
				}
				return reasoningDecision(kind), nil
			})
			runtime := &AgentRuntime{Provider: &coreTestProvider{}, Model: reasoningTestModel{base}}
			if scenario == "unsupported model" {
				runtime.Model = base
			}
			if scenario == "last round" {
				runtime.MaxModelRounds = 1
			}
			if scenario == "completion review" {
				runtime.CompletionReviewPolicy = func(context.Context, agentcontract.CompletionContext) bool { return true }
			}
			result, err := runtime.Run(t.Context(), "task")
			want := 2
			if scenario == "last round" {
				want = 1
			}
			if scenario == "repeated" || scenario == "completion review" {
				want = 3
			}
			if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != "model_decision_invalid" || calls != want || len(result.ModelCalls) != want || result.ModelUsage.BudgetTokens != int64(want*70) || len(result.Observations) != 0 {
				t.Fatal("unavailable control bypassed recovery or usage", calls, result, err)
			}
		})
	}
}

func TestReasoningUpgradeRespectsCancellationAndTokenBudget(t *testing.T) {
	for _, scenario := range []string{"cancel during", "cancel after", "budget after"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			model := reasoningTestModel{func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
				calls++
				if scenario == "cancel during" {
					cancel()
				}
				return reasoningDecision("request_reasoning"), nil
			}}
			runtime := &AgentRuntime{Provider: &coreTestProvider{}, Model: model}
			state, err := runtime.NewState("task", "")
			if err != nil {
				t.Fatal(err)
			}
			err = runtime.Step(ctx, state, nil)
			if scenario == "cancel during" {
				if !errors.Is(err, context.Canceled) || state.ReasoningRequested {
					t.Fatal("cancelled decision changed mode", err, state)
				}
			} else {
				if err != nil || !state.ReasoningRequested {
					t.Fatal("valid upgrade failed", err, state)
				}
				if scenario == "cancel after" {
					cancel()
				} else {
					runtime.MaxModelTokens = 70
				}
				err = runtime.Step(ctx, state, nil)
				if scenario == "cancel after" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost", err)
				}
				if scenario == "budget after" && (err != nil || state.ErrorCode == nil || *state.ErrorCode != "model_token_budget_exhausted") {
					t.Fatal("upgrade bypassed token budget", err, state)
				}
			}
			if calls != 1 || len(state.ModelCalls) != 1 || state.ModelUsage.BudgetTokens != 70 || state.AnswerMarkdown != "" {
				t.Fatal("cancel or budget dispatched another call or lost usage", state)
			}
		})
	}
}
