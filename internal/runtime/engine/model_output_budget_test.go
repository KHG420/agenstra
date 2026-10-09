package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestModelEmptyOutputAtEffectiveTokenLimit(t *testing.T) {
	for _, tc := range []struct {
		name, content, finish, want string
		configured, packetLimit     int
		remaining, output           int64
		omitUsage                   bool
	}{
		{name: "reasoning consumed limit", configured: 4096, output: 4096, finish: "stop", want: "model_output_truncated"},
		{name: "whitespace at limit", configured: 4096, output: 4096, content: " \n\t", finish: "stop", want: "model_output_truncated"},
		{name: "packet limit", configured: 4096, packetLimit: 256, output: 256, finish: "stop", want: "model_output_truncated"},
		{name: "remaining run budget", configured: 4096, remaining: 100, output: 90, finish: "stop", want: "model_output_truncated"},
		{name: "empty below limit", configured: 4096, output: 4095, finish: "stop", want: "model_decision_invalid"},
		{name: "unknown limit", output: 4096, finish: "stop", want: "model_decision_invalid"},
		{name: "missing usage", configured: 4096, omitUsage: true, finish: "stop", want: "model_decision_invalid"},
		{name: "invalid usage", configured: 4096, output: 1000000001, finish: "stop", want: "model_decision_invalid"},
		{name: "provider reports truncation", configured: 4096, output: 20, finish: "length", content: `{"kind":"final","answer_markdown":"partial"}`, want: "model_output_truncated"},
		{name: "complete output at limit", configured: 4096, output: 4096, finish: "stop", content: `{"kind":"final","answer_markdown":"done","fact_ids":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request JSON
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if tc.remaining > 0 && request["max_tokens"] != float64(tc.remaining-10) {
					t.Errorf("remaining budget not applied: %v", request["max_tokens"])
				}
				reply := JSON{"choices": []any{JSON{"finish_reason": tc.finish, "message": JSON{"content": tc.content}}}}
				if !tc.omitUsage {
					reply["usage"] = JSON{"prompt_tokens": 10, "completion_tokens": tc.output, "completion_tokens_details": JSON{"reasoning_tokens": tc.output}}
				}
				if err := json.NewEncoder(w).Encode(reply); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.MaxOutputTokens = tc.configured
			model.CountInputTokens = func(string, []byte) (int64, error) { return 10, nil }
			decision, err := model.Decide(t.Context(), ContextPacket{MaxModelOutputTokens: tc.packetLimit, ModelTokensRemaining: tc.remaining}, "system")
			if ErrorCode(err) != tc.want || requests != 1 {
				t.Fatalf("error=%v, requests=%d, want=%s", err, requests, tc.want)
			}
			if tc.want == "model_output_truncated" && (decision.ModelCall.FormatError != "" || decision.ModelCall.FinishReason != tc.finish || decision.ModelCall.OutputTokens != tc.output) {
				t.Fatalf("truncation lost usage or became a format error: %+v", decision.ModelCall)
			}
		})
	}
}

func TestRuntimeOutputRecoveryPreservesSuccessfulWrite(t *testing.T) {
	capability := CapabilityDescription{Name: "records.create", Version: "1", Effect: "write", Replay: "never", InputSchema: JSON{"type": "object"}}
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{capability.Name: capability}}
	calls := 0
	model := decisionModelFunc(func(_ context.Context, packet ContextPacket, prompt string) (Decision, error) {
		calls++
		metrics := &ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 10, OutputTokens: 20}
		if calls == 1 {
			return Decision{Kind: "tool_batch", Calls: []ToolCall{{CallRef: "create", Capability: capability.Name, Arguments: JSON{}, Reason: "create requested record"}}, ModelCall: metrics}, nil
		}
		if len(packet.Facts) != 1 || len(packet.ActionOutcomes) != 1 || packet.ActionOutcomes[0].Status != "succeeded" {
			t.Fatal("successful write evidence was lost during recovery")
		}
		if calls == 2 {
			metrics.OutputTokens = 4096
			return Decision{ModelCall: metrics}, ModelDecisionError{"model_output_truncated"}
		}
		if calls == 3 && (!strings.Contains(prompt, "output token limit") || strings.Contains(prompt, "not a valid") || !strings.Contains(prompt, "Never repeat a successful write")) {
			t.Fatal("output exhaustion did not receive specific recovery guidance")
		}
		return Decision{Kind: "final", AnswerMarkdown: "created", FactIDs: []string{packet.Facts[0].FactID}, ModelCall: metrics}, nil
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
			model := decisionModelFunc(func(context.Context, ContextPacket, string) (Decision, error) {
				calls++
				return Decision{ModelCall: &ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 10, OutputTokens: 100}}, ModelDecisionError{"model_output_truncated"}
			})
			runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: model, MaxModelRounds: tc.rounds, MaxModelTokens: tc.tokens}
			result, err := runtime.Run(t.Context(), "answer briefly")
			if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != tc.want || calls != tc.wantCalls || result.ModelUsage.OutputTokens != int64(calls)*100 {
				t.Fatalf("budget: status=%s error=%v code=%v calls=%d usage=%+v", result.Status, err, result.ErrorCode, calls, result.ModelUsage)
			}
		})
	}
}

func TestRuntimeOutputRecoveryCannotWriteDuringCompletionReview(t *testing.T) {
	capability := CapabilityDescription{Name: "records.create", Version: "1", Effect: "write", Replay: "never", InputSchema: JSON{"type": "object"}}
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{capability.Name: capability}}
	calls := 0
	model := decisionModelFunc(func(_ context.Context, packet ContextPacket, prompt string) (Decision, error) {
		calls++
		switch calls {
		case 1:
			return Decision{Kind: "tool_batch", Calls: []ToolCall{{CallRef: "create", Capability: capability.Name, Arguments: JSON{}, Reason: "create requested record"}}}, nil
		case 2:
			return Decision{Kind: "final", AnswerMarkdown: "created", FactIDs: []string{packet.Facts[0].FactID}}, nil
		case 3:
			if packet.CompletionReview == nil {
				t.Fatal("expected completion review")
			}
			return Decision{}, ModelDecisionError{"model_output_truncated"}
		default:
			if packet.CompletionReview == nil || !strings.Contains(prompt, "return only a final decision") {
				t.Fatal("recovery lost the completion-review restriction")
			}
			return Decision{Kind: "tool_batch", Calls: []ToolCall{{CallRef: "repeat", Capability: capability.Name, Arguments: JSON{}, Reason: "attempt to repeat write"}}}, nil
		}
	})
	runtime := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{capability.Name: true}}
	result, err := runtime.Run(t.Context(), "create one record")
	if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != "model_decision_invalid" || calls != 4 || provider.called != 1 || len(result.Facts) != 1 {
		t.Fatalf("review restriction: status=%s error=%v code=%v model=%d writes=%d facts=%d", result.Status, err, result.ErrorCode, calls, provider.called, len(result.Facts))
	}
}

func TestMemoryOutputExhaustionRetainsUsageWithoutRecovery(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["max_tokens"] != float64(8192) {
			t.Errorf("unexpected memory output limit: %v", request["max_tokens"])
		}
		reply := JSON{"choices": []any{JSON{"finish_reason": "stop", "message": JSON{"content": ""}}}, "usage": JSON{"prompt_tokens": 10, "completion_tokens": 8192, "completion_tokens_details": JSON{"reasoning_tokens": 8192}}}
		if err := json.NewEncoder(w).Encode(reply); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.MaxOutputTokens = 16384
	proposals, metrics, err := model.ExtractMemoriesMeasured(t.Context(), MemoryExtractionRequest{Text: "Use Chinese in reports.", MaxOutputTokens: 8192})
	if ErrorCode(err) != "model_output_truncated" || len(proposals) != 0 || requests != 1 || metrics.Attempts != 1 || metrics.OutputTokens != 8192 || metrics.ReasoningOutputTokens == nil || *metrics.ReasoningOutputTokens != 8192 || metrics.FormatError != "" || metrics.FormatRecovery {
		t.Fatalf("memory exhaustion: error=%v proposals=%v requests=%d metrics=%+v", err, proposals, requests, metrics)
	}
}
