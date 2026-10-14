package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestModelUsageIncludesReasoningInFallbackWithoutReportingIt(t *testing.T) {
	const answer = `{"kind":"final","answer_markdown":"hello","fact_ids":[]}`
	reasoning := strings.Repeat("private reasoning 推理\n", 128)
	for _, mode := range []string{"json_object", "output_tools"} {
		for _, tc := range []struct {
			name     string
			usage    agentcontract.JSON
			reported bool
		}{
			{name: "missing"},
			{name: "incomplete", usage: agentcontract.JSON{"prompt_tokens": 100}},
			{name: "invalid", usage: agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": -1}},
			{name: "reported", usage: agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 20, "completion_tokens_details": agentcontract.JSON{"reasoning_tokens": 12}}, reported: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				message := agentcontract.JSON{"content": answer, "reasoning_content": reasoning}
				if mode == "output_tools" {
					message["content"] = ""
					message["tool_calls"] = []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": answer}}}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					response := agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": message}}}
					if tc.usage != nil {
						response["usage"] = tc.usage
					}
					if err := json.NewEncoder(w).Encode(response); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				model.DecisionOutputMode = mode
				decision, err := model.Decide(t.Context(), agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "json")
				if err != nil || decision.AnswerMarkdown != "hello" || decision.ModelCall == nil {
					t.Fatalf("decision=%+v error=%v", decision, err)
				}
				metrics := *decision.ModelCall
				wantOutput := int64(len(answer) + len(reasoning))
				if metrics.EstimatedOutputTokens != wantOutput || metrics.UsageAvailable != tc.reported {
					t.Fatalf("estimated output=%d want=%d reported=%t", metrics.EstimatedOutputTokens, wantOutput, metrics.UsageAvailable)
				}
				var usage agentcontract.ModelUsage
				reactcore.AddModelUsage(&usage, metrics)
				if tc.reported {
					if usage.BudgetTokens != 120 || usage.OutputTokens != 20 || usage.ReasoningOutputTokens == nil || *usage.ReasoningOutputTokens != 12 || usage.EstimatedRequests != 0 {
						t.Fatalf("reported usage counted twice or changed: %+v", usage)
					}
				} else if usage.BudgetTokens != metrics.EstimatedInputTokens+wantOutput || usage.OutputTokens != 0 || usage.ReasoningOutputTokens != nil || metrics.ReasoningOutputTokens != nil || usage.ReportedRequests != 0 || usage.EstimatedRequests != 1 {
					t.Fatalf("fallback lost reasoning or fabricated provider usage: %+v", usage)
				}
				raw, err := json.Marshal(decision)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "private reasoning") || strings.Contains(string(raw), "reasoning_content") {
					t.Fatal("reasoning leaked into the decision")
				}
			})
		}
	}
}
