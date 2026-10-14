package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompactCompletionApprovalHTTPStageAndStrictPayload(t *testing.T) {
	for _, mode := range []string{"json_object", "json_schema", "output_tools"} {
		for _, tc := range []struct {
			name, body   string
			review, want bool
		}{
			{"approved", `{"schema":"agenstra.decision.v1","kind":"approve_completion"}`, true, true},
			{"ordinary", `{"schema":"agenstra.decision.v1","kind":"approve_completion"}`, false, false},
			{"payload", `{"schema":"agenstra.decision.v1","kind":"approve_completion","answer_markdown":"unchecked"}`, true, false},
			{"missing feedback", `{"schema":"agenstra.decision.v1","kind":"final","answer_markdown":"draft","fact_ids":[]}`, true, false},
			{"rejected", `{"schema":"agenstra.decision.v1","kind":"final","answer_markdown":"draft","fact_ids":[],"completion_feedback":"Missing original read"}`, true, true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					var payload agentcontract.JSON
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					if mode != "json_object" {
						var schema agentcontract.JSON
						if mode == "output_tools" {
							schema = payload["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
						} else {
							schema = payload["response_format"].(map[string]any)["json_schema"].(map[string]any)["schema"].(map[string]any)
						}
						encoded, err := json.Marshal(schema)
						if err != nil {
							t.Error(err)
							return
						}
						if strings.Contains(string(encoded), "approve_completion") != tc.review {
							t.Error("approval availability does not match stage")
						}
						if tc.review && (strings.Contains(string(encoded), "tool_batch") || strings.Contains(string(encoded), "inspect_fact")) {
							t.Error("review offers execution decisions")
						}
					}
					message := agentcontract.JSON{"content": tc.body}
					if mode == "output_tools" {
						message = agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": strings.ReplaceAll(tc.body, `"schema":"agenstra.decision.v1",`, "")}}}}
					}
					if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": message, "finish_reason": "stop"}}}); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				model.DecisionOutputMode = mode
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
				if tc.review {
					packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft"}
				}
				d, err := model.Decide(t.Context(), packet, "Review proposed answer")
				if (err == nil) != tc.want {
					t.Fatalf("stage/payload accepted=%v want=%v: %v", err == nil, tc.want, err)
				}
				if d.ModelCall == nil || d.ModelCall.Attempts != 1 {
					t.Fatal("lost request accounting")
				}
			})
		}
	}
}
