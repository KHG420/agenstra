package modelapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeConversationTextPreservesThinkingAndStreaming(t *testing.T) {
	for _, mode := range []struct {
		thinking  string
		streaming bool
	}{{"enabled", false}, {"enabled", true}, {"disabled", false}, {"disabled", true}} {
		streaming := mode.streaming
		t.Run(fmt.Sprintf("thinking=%s/stream=%t", mode.thinking, streaming), func(t *testing.T) {
			var requests atomic.Int32
			release := make(chan struct{})
			defer close(release)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if payload["tool_choice"] != "auto" || payload["thinking"].(map[string]any)["type"] != mode.thinking {
					t.Error("configured thinking or native tool choice changed")
				}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					streamFrame(t, w, agentcontract.JSON{"choices": []any{agentcontract.JSON{"index": 0, "delta": agentcontract.JSON{"content": "JSON 是一种", "reasoning_content": "private reasoning"}}}})
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					streamFrame(t, w, agentcontract.JSON{"choices": []any{agentcontract.JSON{"index": 0, "delta": agentcontract.JSON{"content": "数据交换格式。"}, "finish_reason": "stop"}}})
					streamFrame(t, w, agentcontract.JSON{"choices": []any{}, "usage": agentcontract.JSON{"prompt_tokens": 40, "completion_tokens": 12}})
					if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
						t.Error(err)
					}
				} else {
					if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": "JSON 是一种数据交换格式。", "reasoning_content": "private reasoning"}, "finish_reason": "stop"}}, "usage": agentcontract.JSON{"prompt_tokens": 40, "completion_tokens": 12}}); err != nil {
						t.Error(err)
					}
				}
			}))
			defer upstream.Close()
			model, err := NewHTTPJSONDecisionModel("test", upstream.URL, "fixture", 3*time.Second, upstream.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.APIType, model.Thinking, model.DecisionOutputMode = "deepseek_chat", mode.thinking, "native_tools"
			model.StreamAnswers, model.MaxAttempts = streaming, 1
			updates := make(chan agentcontract.AnswerUpdate, 8)
			ctx := agentcontract.WithAnswerObserver(t.Context(), func(u agentcontract.AnswerUpdate) { updates <- u })
			result := make(chan agentcontract.Decision, 1)
			failure := make(chan error, 1)
			go func() {
				d, e := model.Decide(ctx, agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "什么是 JSON？"}, "Use declared tools for business operations.")
				result <- d
				failure <- e
			}()
			if streaming {
				select {
				case update := <-updates:
					if update.Text != "JSON 是一种" || update.Phase != "generating" {
						t.Error("ordinary reply did not stream safely", update)
					}
					release <- struct{}{}
				case <-time.After(time.Second):
					t.Error("ordinary reply did not arrive before completion")
					release <- struct{}{}
				}
			}
			d := <-result
			if err := <-failure; err != nil {
				t.Fatal("ordinary native conversation was rejected", err)
			}
			if d.Kind != "final" || d.AnswerMarkdown != "JSON 是一种数据交换格式。" || len(d.FactIDs) != 0 || requests.Load() != 1 || d.ModelCall.FormatError != "" || !d.ModelCall.UsageAvailable || d.ModelCall.OutputTokens != 12 {
				t.Fatal("reply lost semantics, usage, or was replayed", d)
			}
		})
	}
}

func TestNativeConversationKeepsExplicitFormatsInFinal(t *testing.T) {
	for _, tc := range []struct {
		name, instruction, answer string
	}{
		{name: "JSON", instruction: "只返回 JSON 对象，不用业务工具。", answer: `{"status":"ready"}`},
		{name: "code block", instruction: "只给我一个代码块。", answer: "```text\nready\n```"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				prompt := payload["messages"].([]any)[0].(map[string]any)["content"].(string)
				if payload["tool_choice"] != "auto" || !strings.Contains(prompt, "requested JSON or code blocks") {
					t.Error("structured display guidance changed the automatic tool choice")
				}
				arguments, err := agentcontract.CanonicalJSON(agentcontract.JSON{"answer_markdown": tc.answer, "fact_ids": []string{}})
				if err != nil {
					t.Error(err)
					return
				}
				message := agentcontract.JSON{"tool_calls": []any{
					agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "final", "arguments": string(arguments)}},
				}}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{
					agentcontract.JSON{"message": message, "finish_reason": "tool_calls"},
				}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			model, err := NewHTTPJSONDecisionModel("test", upstream.URL, "fixture", time.Second, upstream.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.APIType, model.Thinking, model.DecisionOutputMode = "deepseek_chat", "disabled", "native_tools"
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: tc.instruction}
			decision, err := model.Decide(t.Context(), packet, "Use declared functions.")
			if err != nil || decision.Kind != "final" || decision.AnswerMarkdown != tc.answer {
				t.Fatal("native final lost the requested display format", decision, err)
			}
		})
	}
}

func TestNativeConversationTextCannotBecomeExecutionOrReview(t *testing.T) {
	for _, mode := range []struct {
		thinking  string
		streaming bool
	}{{"enabled", false}, {"enabled", true}, {"disabled", false}, {"disabled", true}} {
		streaming := mode.streaming
		for _, tc := range []struct {
			name, content, finish string
			review, facts, tool   bool
		}{
			{name: "decision JSON", content: `{"schema":"agenstra.decision.v1","kind":"tool_batch","calls":[]}`, finish: "stop"},
			{name: "approval JSON", content: `{"kind":"approve_completion"}`, finish: "stop"},
			{name: "fenced call", content: "```json\n{\"kind\":\"tool_batch\"}\n```", finish: "stop"},
			{name: "review prose", content: "Everything succeeded", finish: "stop", review: true},
			{name: "evidence needs citations", content: "The verified value is 42", finish: "stop", facts: true},
			{name: "unregistered function", content: "hello", finish: "tool_calls", tool: true},
			{name: "truncated", content: "hello", finish: "length"},
			{name: "filtered", content: "hello", finish: "content_filter"},
		} {
			t.Run(fmt.Sprintf("%s/thinking=%s/stream=%t", tc.name, mode.thinking, streaming), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					message := agentcontract.JSON{"content": tc.content}
					if tc.tool {
						message["tool_calls"] = []any{agentcontract.JSON{"index": 0, "type": "function", "function": agentcontract.JSON{"name": "unknown", "arguments": "{}"}}}
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						streamFrame(t, w, agentcontract.JSON{"choices": []any{agentcontract.JSON{"index": 0, "delta": message, "finish_reason": tc.finish}}})
						if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
							t.Error(err)
						}
					} else {
						if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": message, "finish_reason": tc.finish}}}); err != nil {
							t.Error(err)
						}
					}
				}))
				defer server.Close()
				model, valueErr1 := NewHTTPJSONDecisionModel("test", server.URL, "fixture", time.Second, server.Client())
				if valueErr1 != nil {
					t.Fatal(valueErr1)
				}
				model.APIType, model.Thinking, model.DecisionOutputMode = "deepseek_chat", mode.thinking, "native_tools"
				model.StreamAnswers, model.MaxAttempts = streaming, 1
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
				if tc.review {
					packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: "ok"}
				}
				if tc.facts {
					packet.Facts = []agentcontract.FactView{{Fact: agentcontract.Fact{FactID: "evidence-1"}}}
				}
				d, err := model.Decide(t.Context(), packet, "Use declared functions.")
				if err == nil || d.Kind != "" {
					t.Fatal("text bypassed structured calls, evidence, or review", d, err)
				}
				if strings.Contains(err.Error(), tc.content) {
					t.Fatal("error leaked upstream content")
				}
			})
		}
	}
}
