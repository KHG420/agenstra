package react

import (
	"context"
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCancelledStepCannotBeginAnotherDecisionAttempt(t *testing.T) {
	for _, code := range []string{"model_decision_invalid", "model_output_truncated"} {
		for _, stop := range []bool{false, true} {
			label := code + map[bool]string{false: "/live caller", true: "/cancelled caller"}[stop]
			t.Run(label, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				modelCalls, checkpoints := 0, 0
				model := decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
					modelCalls++
					metrics := &agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 50, OutputTokens: 20}
					if modelCalls == 1 {
						if stop {
							cancel()
						}
						return agentcontract.Decision{ModelCall: metrics}, agentcontract.ModelDecisionError{Kind: code}
					}
					return agentcontract.Decision{Kind: "final", AnswerMarkdown: "hello", FactIDs: []string{}, ModelCall: metrics}, nil
				})
				runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelTokens: 10000}
				state, err := runtime.NewState("reply", "")
				if err != nil {
					t.Fatal(err)
				}
				err = runtime.Step(ctx, state, func() error { checkpoints++; return nil })
				wantCalls := 2
				if stop {
					wantCalls = 1
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancelled Step swallowed cancellation: model=%d checkpoints=%d rounds=%d err=%v", modelCalls, checkpoints, state.RoundsUsed, err)
					}
				} else if err != nil || state.Status != "completed" || state.AnswerMarkdown != "hello" {
					t.Fatalf("live caller lost bounded recovery: %+v %v", state, err)
				}
				if modelCalls != wantCalls || checkpoints != wantCalls || state.RoundsUsed != wantCalls || len(state.ModelCalls) != wantCalls || state.ModelUsage.BudgetTokens != int64(wantCalls*70) || len(state.Observations) != 0 {
					t.Fatalf("cancel scheduled another model attempt or changed usage: model=%d checkpoints=%d rounds=%d calls=%+v usage=%+v", modelCalls, checkpoints, state.RoundsUsed, state.ModelCalls, state.ModelUsage)
				}
				if stop && state.AnswerMarkdown != "" {
					t.Fatal("cancelled attempt published an answer")
				}
				first := state.ModelCalls[0]
				if first.ErrorCode == nil || *first.ErrorCode != code || first.Reservation || first.InputTokens != 50 || first.OutputTokens != 20 {
					t.Fatal("received model diagnostics or usage lost", first)
				}
			})
		}
	}
}

func TestAlreadyCancelledStepDoesNotReserveOrDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls, checkpoints := 0, 0
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
		calls++
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "unexpected", FactIDs: []string{}}, nil
	}), MaxModelTokens: 10000}
	state, err := runtime.NewState("reply", "")
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Step(ctx, state, func() error { checkpoints++; return nil })
	if !errors.Is(err, context.Canceled) || calls != 0 || checkpoints != 0 || state.RoundsUsed != 0 || len(state.ModelCalls) != 0 || state.ModelUsage.BudgetTokens != 0 || state.AnswerMarkdown != "" {
		t.Fatalf("cancelled Step dispatched or reserved: calls=%d checkpoints=%d state=%+v err=%v", calls, checkpoints, state, err)
	}
}

func TestCheckpointCancellationStopsDecisionBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
		calls++
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "unexpected", FactIDs: []string{}}, nil
	}), MaxModelTokens: 10000}
	state, err := runtime.NewState("reply", "")
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Step(ctx, state, func() error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || calls != 0 || state.RoundsUsed != 1 || len(state.ModelCalls) != 1 || state.ModelCalls[0].Attempts != 0 || state.ModelUsage.BudgetTokens != 0 || state.ModelUsage.Requests != 0 || state.AnswerMarkdown != "" {
		t.Fatalf("checkpoint cancellation still dispatched: calls=%d state=%+v err=%v", calls, state, err)
	}
}
