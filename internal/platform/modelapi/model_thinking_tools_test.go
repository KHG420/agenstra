package modelapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestDeepSeekThinkingToolChoice(t *testing.T) {
	for _, mode := range []string{"output_tools", "native_tools"} {
		for _, tc := range []struct {
			name, thinking        string
			review, stream, prose bool
		}{
			{name: "decision", thinking: "enabled"},
			{name: "review", thinking: "enabled", review: true},
			{name: "default thinking review", review: true},
			{name: "disabled review", thinking: "disabled", review: true},
			{name: "streamed review", thinking: "enabled", review: true, stream: true},
			{name: "reject content-only review", thinking: "enabled", review: true, prose: true},
			{name: "reject streamed content-only review", thinking: "enabled", review: true, stream: true, prose: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					var payload agentcontract.JSON
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					thinking := true
					if value, ok := payload["thinking"].(map[string]any); ok {
						thinking = value["type"] != "disabled"
					}
					// Reproduce the provider's rejection before any tokens are generated.
					if thinking && payload["tool_choice"] != "auto" {
						http.Error(w, `{"error":{"message":"Thinking mode does not support this tool_choice","type":"invalid_request_error","code":"invalid_request_error"}}`, http.StatusBadRequest)
						return
					}
					if payload["parallel_tool_calls"] != false || payload["response_format"] != nil || payload["max_tokens"] != float64(4096) {
						t.Error("tool transport or output budget changed")
					}
					if tc.thinking != "" && payload["thinking"].(map[string]any)["type"] != tc.thinking {
						t.Error("adapter changed the configured thinking mode")
					}
					if !thinking {
						want := `{"function":{"name":"submit_decision"},"type":"function"}`
						if mode == "native_tools" {
							want = `"required"`
						}
						choice, err := agentcontract.CanonicalJSON(payload["tool_choice"])
						if err != nil || string(choice) != want {
							t.Error("disabled thinking lost the existing forced tool choice", err)
						}
					}
					kind, function, arguments := "final", "submit_decision", `{"kind":"final","answer_markdown":"ok","fact_ids":[]}`
					if tc.review {
						kind, arguments = "approve_completion", `{"kind":"approve_completion"}`
					}
					if mode == "native_tools" {
						function, arguments = kind, `{"answer_markdown":"ok","fact_ids":[]}`
						if tc.review {
							arguments = `{}`
						}
					}
					call := agentcontract.JSON{"id": "call_test", "type": "function", "function": agentcontract.JSON{"name": function, "arguments": arguments}}
					message := agentcontract.JSON{"tool_calls": []any{call}, "reasoning_content": "private reasoning"}
					finish := "tool_calls"
					if tc.prose {
						message, finish = agentcontract.JSON{"content": `{"kind":"approve_completion"}`}, "stop"
					}
					usage := agentcontract.JSON{"prompt_tokens": 40, "completion_tokens": 12}
					if tc.stream {
						call["index"] = 0
						w.Header().Set("Content-Type", "text/event-stream")
						streamFrame(t, w, agentcontract.JSON{"choices": []any{agentcontract.JSON{"index": 0, "delta": message, "finish_reason": finish}}})
						streamFrame(t, w, agentcontract.JSON{"choices": []any{}, "usage": usage})
						if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
							t.Error(err)
						}
						return
					}
					if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": message, "finish_reason": finish}}, "usage": usage}); err != nil {
						t.Error(err)
					}
				}))
				defer upstream.Close()
				model, err := NewHTTPJSONDecisionModel("arbitrary-model-name", upstream.URL, "test", time.Second, upstream.Client())
				if err != nil {
					t.Fatal(err)
				}
				model.APIType, model.Thinking, model.DecisionOutputMode = "deepseek_chat", tc.thinking, mode
				model.MaxAttempts, model.MaxOutputTokens, model.StreamAnswers = 1, 4096, tc.stream
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
				if tc.review {
					packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: "ok", FactIDs: []string{}}
				}
				decision, err := model.Decide(t.Context(), packet, "Return one declared output tool.")
				if tc.prose {
					if agentcontract.ModelErrorCode(err) != "model_decision_invalid" || decision.Kind != "" {
						t.Fatal("auto tool choice must not accept content-only decisions", decision, err)
					}
				} else if err != nil || tc.review && decision.Kind != "approve_completion" || !tc.review && decision.Kind != "final" {
					t.Fatal("provider rejected the configured tool protocol", decision, err)
				}
				if requests.Load() != 1 || !decision.ModelCall.UsageAvailable || decision.ModelCall.OutputTokens != 12 {
					t.Fatal("request was replayed or lost usage", requests.Load(), decision.ModelCall)
				}
			})
		}
	}
}
