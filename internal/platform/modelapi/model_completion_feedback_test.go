package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestHTTPCompletionReviewRequiresExplicitFeedback(t *testing.T) {
	for _, mode := range []string{"json_object", "output_tools"} {
		for _, tc := range []struct {
			name string
			body agentcontract.JSON
			want bool
		}{
			{"complete", agentcontract.JSON{"kind": "final", "answer_markdown": "Completed", "fact_ids": []string{}, "completion_feedback": ""}, true},
			{"incomplete", agentcontract.JSON{"kind": "final", "answer_markdown": "draft", "fact_ids": []string{}, "completion_feedback": "Requested read is missing."}, true},
			{"omitted", agentcontract.JSON{"kind": "final", "answer_markdown": "Completed", "fact_ids": []string{}}, false},
			{"wrong stage", agentcontract.JSON{"kind": "inspect_capability", "name": "records.read"}, false},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					body, err := agentcontract.CanonicalJSON(tc.body)
					if err != nil {
						t.Error(err)
						return
					}
					message := agentcontract.JSON{"content": string(body)}
					if mode == "output_tools" {
						message = agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": string(body)}}}}
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
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft", FactIDs: []string{}}}
				decision, err := model.Decide(t.Context(), packet, "Review completion")
				if (err == nil) != tc.want {
					t.Fatalf("review accepted=%v, want=%v: %+v %v", err == nil, tc.want, decision, err)
				}
				if !tc.want && (agentcontract.ErrorCode(err) != "model_decision_invalid" || decision.ModelCall == nil || decision.ModelCall.FormatError != "model_decision_schema_invalid") {
					t.Fatal("missing review contract was not accounted as invalid", decision, err)
				}
				if tc.name == "omitted" {
					packet.CompletionReview = nil
					if _, err := model.Decide(t.Context(), packet, "Normal decision"); err != nil {
						t.Fatal("normal final now requires reviewer feedback", err)
					}
				}
			})
		}
	}
}
