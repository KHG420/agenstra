package react

import (
	"context"
	"errors"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestTransientRuntimePropagatesCallerModelCancellation(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[expired], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if expired {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				want = context.DeadlineExceeded
			}
			defer cancel()
			calls := 0
			model := decisionModelFunc(func(ctx context.Context, _ agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				calls++
				if !expired {
					cancel()
				}
				<-ctx.Done()
				return agentcontract.Decision{ModelCall: &agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: 100}}, ctx.Err()
			})
			runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelTokens: 10000}
			result, err := runtime.Run(ctx, "reply")
			if !errors.Is(err, want) || result.Status != "failed" || calls != 1 || len(result.Observations) != 0 {
				t.Fatalf("caller cancellation was swallowed or execution continued: status=%s calls=%d err=%v", result.Status, calls, err)
			}
			if len(result.ModelCalls) != 1 || !result.ModelCalls[0].Reservation || result.ModelUsage.BudgetTokens != 10000 {
				t.Fatalf("cancellation lost unknown dispatched budget: calls=%+v usage=%+v", result.ModelCalls, result.ModelUsage)
			}
		})
	}
}

func TestTransientRuntimeCancelledModelCannotDispatchReturnedCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	provider := &hostProvider{}
	model := decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
		cancel()
		return agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "read-1", Capability: "records.get", Arguments: agentcontract.JSON{"id": "A"}, Reason: "Read"}}}, nil
	})
	runtime := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"records.get": true}}
	result, err := runtime.Run(ctx, "read")
	if !errors.Is(err, context.Canceled) || provider.calls != 0 || len(result.Facts) != 0 || len(result.Observations) != 0 {
		t.Fatalf("cancelled caller dispatched a returned call: provider=%d result=%+v err=%v", provider.calls, result, err)
	}
}

func TestTransientSerialCancellationRetainsOutcomeAndStopsRemainingCalls(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			provider := &hostProvider{hook: func(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
				cancel()
				switch outcome {
				case "succeeded":
					return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "A"}}, nil
				case "failed":
					return agentcontract.CapabilityResult{ErrorCode: "business_failed"}, nil
				default:
					return agentcontract.CapabilityResult{}, context.Canceled
				}
			}}
			model := &hostModel{decisions: []agentcontract.Decision{{Kind: "tool_batch", Calls: []agentcontract.ToolCall{
				{CallRef: "read-1", Capability: "records.get", Arguments: agentcontract.JSON{"id": "A"}, Reason: "Read A"},
				{CallRef: "read-2", Capability: "records.get", Arguments: agentcontract.JSON{"id": "B"}, Reason: "Read B"},
			}}}}
			runtime := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"records.get": true}, MaxConcurrentTools: 1}
			result, err := runtime.Run(ctx, "read both")
			if !errors.Is(err, context.Canceled) || provider.calls != 1 || model.calls != 1 || len(result.Observations) != 1 || result.AnswerMarkdown != "" {
				t.Fatalf("cancelled sequential execution continued or lost result: provider=%d model=%d result=%+v err=%v", provider.calls, model.calls, result, err)
			}
			observation := result.Observations[0]
			switch outcome {
			case "succeeded":
				if observation.Status != "succeeded" || len(result.Facts) != 1 || result.Facts[0].Value["data"].(agentcontract.JSON)["id"] != "A" {
					t.Fatal("known successful receipt lost", result)
				}
			case "failed":
				if observation.Status != "failed" || observation.ErrorCode == nil || *observation.ErrorCode != "business_failed" {
					t.Fatal("known failure changed", result)
				}
			default:
				if result.Status != "needs_reconciliation" || observation.ErrorCode == nil || *observation.ErrorCode != "provider_outcome_unknown" {
					t.Fatal("uncertain outcome changed", result)
				}
			}
		})
	}
}
