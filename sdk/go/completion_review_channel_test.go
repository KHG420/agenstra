package agenstra_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

func TestSDKCompletionReviewKeepsConfiguredToolChannel(t *testing.T) {
	for _, mode := range []string{"output_tools", "native_tools"} {
		for _, recoverFormat := range []bool{false, true} {
			name := mode + "/initial"
			if recoverFormat {
				name = mode + "/format-recovery"
			}
			t.Run(name, func(t *testing.T) {
				var reviews atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var payload agenstra.JSON
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					messages := payload["messages"].([]any)
					prompt := messages[0].(map[string]any)["content"].(string)
					var packet agenstra.ContextPacket
					if err := json.Unmarshal([]byte(messages[1].(map[string]any)["content"].(string)), &packet); err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					kind, arguments := "final", agenstra.JSON{"answer_markdown": "Verified explanation.", "fact_ids": []string{}}
					if packet.CompletionReview != nil {
						count := reviews.Add(1)
						// Reproduce the real conflict: the runtime's inline JSON verdict
						// example overrode the adapter's declared output-tool channel.
						conflict := strings.Contains(prompt, `return {"schema":"agenstra.decision.v1","kind":"approve_completion"}`) ||
							strings.Contains(prompt, "return exactly one final typed agenstra.decision.v1 decision")
						if conflict || recoverFormat && count == 1 {
							if err := json.NewEncoder(w).Encode(agenstra.JSON{"choices": []any{agenstra.JSON{"finish_reason": "stop", "message": agenstra.JSON{"content": `{"schema":"agenstra.decision.v1","kind":"approve_completion"}`}}}}); err != nil {
								t.Error(err)
							}
							return
						}
						if payload["tool_choice"] != "auto" || !strings.Contains(prompt, "numerical relationships") || !strings.Contains(prompt, "completion_feedback") {
							t.Error("review lost thinking-compatible protocol or substantive review rules")
						}
						kind, arguments = "approve_completion", agenstra.JSON{}
					}
					function := kind
					if mode == "output_tools" {
						function, arguments["kind"] = "submit_decision", kind
					}
					nativeSDKReply(t, w, function, arguments)
				}))
				defer server.Close()
				model, err := agenstra.NewHTTPJSONDecisionModel("test", server.URL, "fixture", 5*time.Second, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				model.APIType, model.Thinking, model.DecisionOutputMode = "deepseek_chat", "enabled", mode
				provider := &nativeInventoryProvider{}
				runtime := &agenstra.AgentRuntime{
					Provider: provider, Model: model, MaxModelRounds: 4,
					CompletionReviewPolicy: func(context.Context, agenstra.CompletionContext) bool { return true },
				}
				result, err := runtime.Run(t.Context(), "Give a general explanation; do not access inventory.")
				wantReviews := int32(1)
				if recoverFormat {
					wantReviews = 2
				}
				if err != nil || result.Status != "completed" || result.AnswerMarkdown != "Verified explanation." || reviews.Load() != wantReviews || len(provider.calls) != 0 {
					t.Fatalf("review did not preserve its channel or replayed business calls: status=%s reviews=%d calls=%d err=%v", result.Status, reviews.Load(), len(provider.calls), err)
				}
			})
		}
	}
}
