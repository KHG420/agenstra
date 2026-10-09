package react

import (
	"context"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRuntimeOutputRecoveryPreservesSuccessfulWrite(t *testing.T) {
	capability := agentcontract.CapabilityDescription{Name: "records.create", Version: "1", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"}}
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{capability.Name: capability}}
	calls := 0
	model := decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		calls++
		metrics := &agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 10, OutputTokens: 20}
		if calls == 1 {
			return agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "create", Capability: capability.Name, Arguments: agentcontract.JSON{}, Reason: "create requested record"}}, ModelCall: metrics}, nil
		}
		if len(packet.Facts) != 1 || len(packet.ActionOutcomes) != 1 || packet.ActionOutcomes[0].Status != "succeeded" {
			t.Fatal("successful write evidence was lost during recovery")
		}
		if calls == 2 {
			metrics.OutputTokens = 4096
			return agentcontract.Decision{ModelCall: metrics}, agentcontract.ModelDecisionError{Kind: "model_output_truncated"}
		}
		if calls == 3 && (!strings.Contains(prompt, "output token limit") || strings.Contains(prompt, "not a valid") || !strings.Contains(prompt, "Never repeat a successful write")) {
			t.Fatal("output exhaustion did not receive specific recovery guidance")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "created", FactIDs: []string{packet.Facts[0].FactID}, ModelCall: metrics}, nil
	})
	runtime := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{capability.Name: true}}
	result, err := runtime.Run(t.Context(), "create one record")
	if err != nil || result.Status != "completed" || calls != 4 || provider.called != 1 || len(result.Facts) != 1 {
		t.Fatalf("recovery/write count: status=%s error=%v model=%d writes=%d facts=%d", result.Status, err, calls, provider.called, len(result.Facts))
	}
	if result.ModelUsage.FormatRecoveryRequests != 0 || result.ModelUsage.OutputTokens != 4156 || result.ModelCalls[1].ErrorCode == nil || *result.ModelCalls[1].ErrorCode != "model_output_truncated" {
		t.Fatalf("recovery usage was lost or classified as format repair: %+v", result.ModelUsage)
	}
}

func TestRuntimeOutputRecoveryStopsAtExistingBudgets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rounds    int
		tokens    int64
		wantCalls int
		want      string
	}{
		{name: "bounded recovery", rounds: 20, wantCalls: 2, want: "model_output_truncated"},
		{name: "round budget", rounds: 1, wantCalls: 1, want: "model_output_truncated"},
		{name: "token budget", rounds: 20, tokens: 110, wantCalls: 1, want: "model_token_budget_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			model := decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
				calls++
				return agentcontract.Decision{ModelCall: &agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 10, OutputTokens: 100}}, agentcontract.ModelDecisionError{Kind: "model_output_truncated"}
			})
			runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelRounds: tc.rounds, MaxModelTokens: tc.tokens}
			result, err := runtime.Run(t.Context(), "answer briefly")
			if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != tc.want || calls != tc.wantCalls || result.ModelUsage.OutputTokens != int64(calls)*100 {
				t.Fatalf("budget: status=%s error=%v code=%v calls=%d usage=%+v", result.Status, err, result.ErrorCode, calls, result.ModelUsage)
			}
		})
	}
}

func TestRuntimeOutputRecoveryCannotWriteDuringCompletionReview(t *testing.T) {
	capability := agentcontract.CapabilityDescription{Name: "records.create", Version: "1", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"}}
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{capability.Name: capability}}
	calls := 0
	model := decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		calls++
		switch calls {
		case 1:
			return agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "create", Capability: capability.Name, Arguments: agentcontract.JSON{}, Reason: "create requested record"}}}, nil
		case 2:
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "created", FactIDs: []string{packet.Facts[0].FactID}}, nil
		case 3:
			if packet.CompletionReview == nil {
				t.Fatal("expected completion review")
			}
			return agentcontract.Decision{}, agentcontract.ModelDecisionError{Kind: "model_output_truncated"}
		default:
			if packet.CompletionReview == nil || !strings.Contains(prompt, "return only a final decision") {
				t.Fatal("recovery lost the completion-review restriction")
			}
			return agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "repeat", Capability: capability.Name, Arguments: agentcontract.JSON{}, Reason: "attempt to repeat write"}}}, nil
		}
	})
	runtime := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{capability.Name: true}}
	result, err := runtime.Run(t.Context(), "create one record")
	if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != "model_decision_invalid" || calls != 4 || provider.called != 1 || len(result.Facts) != 1 {
		t.Fatalf("review restriction: status=%s error=%v code=%v model=%d writes=%d facts=%d", result.Status, err, result.ErrorCode, calls, provider.called, len(result.Facts))
	}
}
