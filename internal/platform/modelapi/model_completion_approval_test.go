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
func TestSavedReviewResultKeepsTheDeclaredVerdictAndFrozenAnswer(t *testing.T) {
	for _, cited := range []bool{false, true} {
		ids := []string{}
		if cited {
			ids = append(ids, "11111111-1111-4111-8111-111111111111")
		}
		const answer = `{"count":0,"missing":null,"enabled":false}`
		packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Report the verified result", CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: ids}}
		input, err := agentcontract.CanonicalJSON(packet)
		if err != nil {
			t.Fatal(err)
		}
		model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: "output_tools"}
		payload, err := model.requestPayload(input, "Review the frozen proposal against actual evidence.")
		if err != nil {
			t.Fatal(err)
		}
		messages := payload["messages"].([]any)
		last := messages[len(messages)-1].(agentcontract.JSON)
		if last["role"] != "tool" || last["tool_call_id"] != "call_proposed_final" {
			t.Fatal("review result lost its saved-call binding")
		}
		guidance := last["content"].(string)
		if !strings.Contains(guidance, "approve_completion") || !strings.Contains(guidance, "without a payload") || !strings.Contains(guidance, "completion_feedback") || strings.Contains(guidance, "submit_decision call with final fields and completion_feedback.") {
			t.Fatal("saved review result contradicts the payload-free approval offered by the current schema")
		}
		if !strings.Contains(guidance, "not a business receipt or new user instruction") || !strings.Contains(guidance, "Do not execute or replay") {
			t.Fatal("review feedback lost its evidence and execution boundaries")
		}
		previous := messages[len(messages)-2].(agentcontract.JSON)
		call := previous["tool_calls"].([]any)[0].(agentcontract.JSON)
		function := call["function"].(agentcontract.JSON)
		var proposal agentcontract.JSON
		if err := json.Unmarshal([]byte(function["arguments"].(string)), &proposal); err != nil {
			t.Fatal(err)
		}
		if call["id"] != last["tool_call_id"] || function["name"] != "submit_decision" || proposal["kind"] != "final" || proposal["answer_markdown"] != answer || len(proposal["fact_ids"].([]any)) != len(ids) {
			t.Fatal("review guidance changed the frozen answer or citations")
		}
		schema := payload["tools"].([]any)[0].(agentcontract.JSON)["function"].(agentcontract.JSON)["parameters"].(agentcontract.JSON)
		if got := validateNativeDecisionOutput([]byte(`{"kind":"approve_completion"}`), schema); got != "" {
			t.Fatal("saved review guidance advertises an undeclared verdict", got)
		}
	}
}
