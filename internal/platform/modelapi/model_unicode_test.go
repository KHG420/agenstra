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

func TestModelUnicodeTextLimitsAgreeAcrossOutputModes(t *testing.T) {
	cases := []struct {
		name   string
		tool   string
		kind   string
		limit  int
		fields func(string) agentcontract.JSON
	}{
		{"reason", "submit_tool_call", "tool_batch", 500, func(text string) agentcontract.JSON {
			return agentcontract.JSON{"capability": "records.read", "arguments": agentcontract.JSON{}, "reason": text}
		}},
		{"answer", "submit_final", "final", 30000, func(text string) agentcontract.JSON {
			return agentcontract.JSON{"answer_markdown": text, "fact_ids": []string{}}
		}},
		{"prompt", "submit_request_input", "request_input", 1000, func(text string) agentcontract.JSON {
			return agentcontract.JSON{"field": "label", "prompt": text}
		}},
		{"result label", "submit_final", "final", 100, func(text string) agentcontract.JSON {
			return agentcontract.JSON{"answer_markdown": "Ready", "fact_ids": []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}, "result_refs": []any{agentcontract.JSON{"fact_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "path": []any{"data", "id"}, "label": text}}}
		}},
	}
	for _, tc := range cases {
		for _, mode := range []string{"json_object", "output_tools"} {
			for _, extra := range []int{0, 1} {
				t.Run(tc.name+"/"+mode+"/"+strings.Repeat("overflow", extra), func(t *testing.T) {
					fields := tc.fields(strings.Repeat("中", tc.limit+extra))
					for _, tool := range decisionOutputTools() {
						function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
						if function["name"] != tc.tool {
							continue
						}
						parameters, err := agentcontract.ObjectOf(function["parameters"])
						if err != nil {
							t.Fatal(err)
						}
						input, err := agentcontract.ObjectOf(fields)
						if err != nil {
							t.Fatal(err)
						}
						schema, err := agentcontract.ValidateLocalSchema(parameters, false)
						if err != nil {
							t.Fatal(err)
						}
						if err := agentcontract.ValidateSchema(schema, input); (err == nil) != (extra == 0) {
							t.Fatalf("advertised output schema disagrees with the character bound: %v", err)
						}
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						var message agentcontract.JSON
						if mode == "output_tools" {
							arguments, err := agentcontract.CanonicalJSON(fields)
							if err != nil {
								t.Error(err)
								return
							}
							message = agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": tc.tool, "arguments": string(arguments)}}}}
						} else {
							var value agentcontract.JSON
							if tc.kind == "tool_batch" {
								fields["call_ref"] = "read-1"
								value = agentcontract.JSON{"calls": []any{fields}}
							} else {
								value = fields
							}
							value["kind"] = tc.kind
							body, err := agentcontract.CanonicalJSON(value)
							if err != nil {
								t.Error(err)
								return
							}
							message = agentcontract.JSON{"content": string(body)}
						}
						if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": message, "finish_reason": "stop"}}}); err != nil {
							t.Error(err)
						}
					}))
					defer server.Close()
					model, err := NewHTTPJSONDecisionModel("unicode-test", server.URL, "test-key", time.Second, server.Client())
					if err != nil {
						t.Fatal(err)
					}
					model.DecisionOutputMode = mode
					decision, err := model.Decide(t.Context(), agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "Return one decision")
					if extra == 0 && (err != nil || decision.Kind != tc.kind || decision.ModelCall == nil || decision.ModelCall.FormatError != "") {
						t.Fatalf("valid Unicode output rejected: decision=%+v, err=%v", decision, err)
					}
					if extra == 1 && agentcontract.ErrorCode(err) != "model_decision_invalid" {
						t.Fatalf("over-limit Unicode output accepted or misclassified: err=%v", err)
					}
				})
			}
		}
	}
}
