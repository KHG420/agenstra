package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestIncompleteOutputToolAtEffectiveTokenLimit(t *testing.T) {
	partial := `{"kind":"final","answer_markdown":"partial`
	complete := `{"kind":"final","answer_markdown":"done","fact_ids":[]}`
	for _, tc := range []struct {
		name, arguments, tool, callType, field, want, format string
		configured, packetLimit, count                       int
		remaining, output                                    int64
		omitUsage                                            bool
	}{
		{name: "unfinished string", configured: 4096, output: 4096, arguments: partial, count: 1, want: "model_output_truncated"},
		{name: "packet limit", configured: 4096, packetLimit: 256, output: 256, arguments: partial, count: 1, want: "model_output_truncated"},
		{name: "remaining budget", configured: 4096, remaining: 100, output: 90, arguments: partial, count: 1, want: "model_output_truncated"},
		{name: "completion token field", configured: 4096, output: 4096, arguments: partial, count: 1, field: "max_completion_tokens", want: "model_output_truncated"},
		{name: "below limit", configured: 4096, output: 4095, arguments: partial, count: 1, want: "model_decision_invalid", format: "model_output_invalid_json"},
		{name: "unknown limit", output: 4096, arguments: partial, count: 1, want: "model_decision_invalid", format: "model_output_invalid_json"},
		{name: "missing usage", configured: 4096, omitUsage: true, arguments: partial, count: 1, want: "model_decision_invalid", format: "model_output_invalid_json"},
		{name: "invalid usage", configured: 4096, output: 1000000001, arguments: partial, count: 1, want: "model_decision_invalid", format: "model_output_invalid_json"},
		{name: "negative usage", configured: 4096, output: -1, arguments: partial, count: 1, want: "model_decision_invalid", format: "model_output_invalid_json"},
		{name: "syntax error", configured: 4096, output: 4096, arguments: `{"kind":unquoted}`, count: 1, want: "model_decision_invalid", format: "model_output_invalid_json"},
		{name: "duplicate keys", configured: 4096, output: 4096, arguments: `{"kind":"final","answer_markdown":"done","fact_ids":[],"fact_ids":[]}`, count: 1, want: "model_decision_invalid", format: "model_output_duplicate_key"},
		{name: "invalid contract", configured: 4096, output: 4096, arguments: `{"kind":"final","answer_markdown":"done","fact_ids":"[]"}`, count: 1, want: "model_decision_invalid", format: "model_decision_schema_invalid"},
		{name: "unknown tool", configured: 4096, output: 4096, arguments: partial, count: 1, tool: "submit_other", want: "model_decision_invalid", format: "model_output_tool_calls_invalid"},
		{name: "multiple tools", configured: 4096, output: 4096, arguments: partial, count: 2, want: "model_decision_invalid", format: "model_output_tool_calls_invalid"},
		{name: "wrong call type", configured: 4096, output: 4096, arguments: partial, count: 1, callType: "other", want: "model_decision_invalid", format: "model_output_tool_calls_invalid"},
		{name: "empty arguments", configured: 4096, output: 4096, count: 1, want: "model_decision_invalid", format: "model_output_invalid_json"},
		{name: "complete at limit", configured: 4096, output: 4096, arguments: complete, count: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			field, tool, callType := tc.field, tc.tool, tc.callType
			if field == "" {
				field = "max_tokens"
			}
			if tool == "" {
				tool = "submit_decision"
			}
			if callType == "" {
				callType = "function"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				assertSingleDecisionOutputRequest(t, request)
				limit := tc.configured
				if tc.packetLimit > 0 {
					limit = tc.packetLimit
				}
				if tc.remaining > 0 {
					limit = int(tc.remaining - 10)
				}
				if limit > 0 && request[field] != float64(limit) {
					t.Errorf("effective output limit changed: %v", request[field])
				}
				calls := make([]any, tc.count)
				for i := range calls {
					calls[i] = agentcontract.JSON{"type": callType, "function": agentcontract.JSON{"name": tool, "arguments": tc.arguments}}
				}
				reply := agentcontract.JSON{"choices": []any{agentcontract.JSON{"finish_reason": "tool_calls", "message": agentcontract.JSON{"content": "non-executable commentary", "tool_calls": calls}}}}
				if !tc.omitUsage {
					reply["usage"] = agentcontract.JSON{"prompt_tokens": 10, "completion_tokens": tc.output}
				}
				if err := json.NewEncoder(w).Encode(reply); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("compatible-test-model", server.URL, "test-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.MaxOutputTokens, model.TokenLimitField, model.DecisionOutputMode = tc.configured, field, "output_tools"
			model.CountInputTokens = func(string, []byte) (int64, error) { return 10, nil }
			decision, err := model.Decide(t.Context(), agentcontract.ContextPacket{Schema: "agenstra.context.v1", MaxModelOutputTokens: tc.packetLimit, ModelTokensRemaining: tc.remaining}, "system")
			metrics := decision.ModelCall
			if agentcontract.ErrorCode(err) != tc.want || requests != 1 || metrics == nil || metrics.FormatError != tc.format {
				t.Fatalf("error=%v requests=%d metrics=%+v want=%s/%s", err, requests, metrics, tc.want, tc.format)
			}
			if metrics.FinishReason != "tool_calls" || tc.want == "model_output_truncated" && metrics.OutputTokens != tc.output {
				t.Fatalf("classification lost original finish reason or usage: %+v", metrics)
			}
			if tc.want == "" && decision.AnswerMarkdown != "done" {
				t.Fatal("complete output at the limit was changed", decision)
			}
		})
	}
}
