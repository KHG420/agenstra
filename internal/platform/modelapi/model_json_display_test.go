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

func TestJSONDecisionKeepsUserFormatsInsideFinal(t *testing.T) {
	for _, tc := range []struct{ name, answer string }{
		{"JSON", `{"count":0,"missing":null,"enabled":false}`},
		{"code block", "```json\n{\"count\":0}\n```"},
		{"table", "| Name | Count |\n| --- | --- |\n| alpha | 0 |"},
	} {
		for _, mode := range []string{"", "json_object"} {
			for _, review := range []bool{false, true} {
				stage := "decision"
				if review {
					stage = "review"
				}
				t.Run(tc.name+"/"+mode+"/"+stage, func(t *testing.T) {
					const hostRule = "Preserve host authorization and never replay a successful write."
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var payload agentcontract.JSON
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
							return
						}
						messages := payload["messages"].([]any)
						prompt := messages[0].(map[string]any)["content"].(string)
						if !strings.HasPrefix(prompt, hostRule) || !strings.Contains(prompt, "apply to final.answer_markdown only") ||
							!strings.Contains(prompt, "never replace the agenstra.decision.v1 JSON decision object") {
							t.Error("JSON display format is not separated from the decision protocol", prompt)
						}
						if payload["tools"] != nil || payload["response_format"].(map[string]any)["type"] != "json_object" {
							t.Error("display guidance changed the configured output channel")
						}
						fields := agentcontract.JSON{"schema": "agenstra.decision.v1", "kind": "final", "answer_markdown": tc.answer, "fact_ids": []string{}}
						if review {
							fields["completion_feedback"] = ""
						}
						content, err := agentcontract.CanonicalJSON(fields)
						if err != nil {
							t.Error(err)
							return
						}
						if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
							"message": agentcontract.JSON{"content": string(content)}, "finish_reason": "stop",
						}}}); err != nil {
							t.Error(err)
						}
					}))
					defer upstream.Close()
					model, err := NewHTTPJSONDecisionModel("compatible-model", upstream.URL, "test-key", time.Second, upstream.Client())
					if err != nil {
						t.Fatal(err)
					}
					model.DecisionOutputMode = mode
					packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Return only the requested display format."}
					if review {
						packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: tc.answer, FactIDs: []string{}}
					}
					before, err := agentcontract.CanonicalJSON(packet)
					if err != nil {
						t.Fatal(err)
					}
					decision, err := model.Decide(t.Context(), packet, hostRule)
					if err != nil || decision.Kind != "final" || decision.AnswerMarkdown != tc.answer {
						t.Fatal("user-facing format changed or replaced the JSON decision", decision, err)
					}
					after, err := agentcontract.CanonicalJSON(packet)
					if err != nil || string(after) != string(before) {
						t.Fatal("display guidance mutated the caller's context")
					}
				})
			}
		}
	}
}

func TestJSONDecisionRejectsUnwrappedBusinessObject(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
			"message": agentcontract.JSON{"content": `{"count":0,"missing":null,"enabled":false}`}, "finish_reason": "stop",
		}}}); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	model, err := NewHTTPJSONDecisionModel("compatible-model", upstream.URL, "test-key", time.Second, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	decision, err := model.Decide(t.Context(), agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Return only raw JSON."}, "")
	if err == nil || decision.ModelCall == nil || decision.ModelCall.FormatError != "model_decision_schema_invalid" {
		t.Fatal("unwrapped business output was accepted or repaired into a decision", decision, err)
	}
}
