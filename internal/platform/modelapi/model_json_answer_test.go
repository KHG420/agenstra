package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestJSONFinalAnswerAcrossOutputModes(t *testing.T) {
	for _, mode := range []string{"json_object", "json_schema", "output_tools", "native_tools"} {
		for _, review := range []bool{false, true} {
			for _, tc := range []struct {
				name, fields, want string
			}{
				{"object", `"answer_json":{"id":9007199254740993,"decimal":9007199254740993.1,"zero":0,"missing":null,"enabled":false}`, `{"id":9007199254740993,"decimal":9007199254740993.1,"zero":0,"missing":null,"enabled":false}`},
				{"array", `"answer_json":[false,null,0,"引号\"、换行\n",{}]`, `[false,null,0,"引号\"、换行\n",{}]`},
				{"null", `"answer_json":null`, `null`},
				{"string", `"answer_json":"raw string"`, `"raw string"`},
				{"markdown retained", `"answer_markdown":"# Report\nVerified"`, "# Report\nVerified"},
				{"both channels", `"answer_json":{},"answer_markdown":"text"`, ""},
				{"empty conflicting channel", `"answer_json":{},"answer_markdown":""`, ""},
				{"null conflicting channel", `"answer_json":{},"answer_markdown":null`, ""},
				{"no answer", `"result_refs":[]`, ""},
				{"unknown alias", `"answer_data":{}`, ""},
				{"duplicate business key", `"answer_json":{"count":1,"count":2}`, ""},
				{"malformed business value", `"answer_json":{"count":1}}`, ""},
			} {
				stage := "decision"
				if review {
					stage = "review"
				}
				t.Run(mode+"/"+stage+"/"+tc.name, func(t *testing.T) {
					requests := 0
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests++
						var payload agentcontract.JSON
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
							return
						}
						prompt := payload["messages"].([]any)[0].(map[string]any)["content"].(string)
						if !strings.Contains(prompt, "answer_json") || strings.Contains(prompt, "Encode business JSON as escaped text") || strings.Contains(prompt, "display formats apply inside answer_markdown only") {
							t.Error("request does not teach the native JSON answer channel")
						}
						raw := `{` + tc.fields + `,"fact_ids":[]`
						if review {
							raw += `,"completion_feedback":""`
						}
						message := agentcontract.JSON{}
						finish := "stop"
						if mode == "output_tools" || mode == "native_tools" {
							name := "final"
							if mode == "output_tools" {
								name = "submit_decision"
								raw += `,"kind":"final"`
							}
							message["tool_calls"] = []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": name, "arguments": raw + `}`}}}
							finish = "tool_calls"
						} else {
							message["content"] = raw + `,"schema":"agenstra.decision.v1","kind":"final"}`
						}
						if err := json.NewEncoder(w).Encode(agentcontract.JSON{"usage": agentcontract.JSON{"prompt_tokens": 10, "completion_tokens": 20}, "choices": []any{agentcontract.JSON{"message": message, "finish_reason": finish}}}); err != nil {
							t.Error(err)
						}
					}))
					defer upstream.Close()
					model, err := NewHTTPJSONDecisionModel("compatible-model", upstream.URL, "test-key", time.Second, upstream.Client())
					if err != nil {
						t.Fatal(err)
					}
					model.DecisionOutputMode = mode
					packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
					if review {
						packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft"}
					}
					decision, err := model.Decide(t.Context(), packet, "Preserve host authorization and evidence.")
					if requests != 1 || decision.ModelCall == nil || decision.ModelCall.Attempts != 1 {
						t.Fatal("answer transport silently retried or lost metrics", requests, decision.ModelCall)
					}
					if tc.want == "" {
						if agentcontract.ErrorCode(err) != "model_decision_invalid" {
							t.Fatal("invalid JSON answer accepted or repaired", decision, err)
						}
					} else {
						if err != nil || decision.Kind != "final" {
							t.Fatal("valid final rejected", decision, err)
						}
						if strings.HasPrefix(tc.fields, `"answer_json"`) {
							var actual, expected any
							if err := jsonvalue.DecodeStrict([]byte(decision.AnswerMarkdown), &actual); err != nil {
								t.Fatal("rendered answer is not valid JSON", err)
							}
							if err := jsonvalue.DecodeStrict([]byte(tc.want), &expected); err != nil {
								t.Fatal(err)
							}
							// Object key ordering is immaterial; DecodeStrict retains
							// json.Number literals, including integers above 2^53.
							if !reflect.DeepEqual(actual, expected) {
								t.Fatal("native JSON value or number literal changed", decision.AnswerMarkdown)
							}
						} else if decision.AnswerMarkdown != tc.want {
							t.Fatal("existing Markdown answer changed", decision.AnswerMarkdown)
						}
					}
				})
			}
		}
	}
}

func TestJSONFinalAnswerSchemasRequireExactlyOneChannel(t *testing.T) {
	for _, review := range []bool{false, true} {
		tools := decisionOutputTools(review)
		unified := singleDecisionOutputTool(tools)["function"].(agentcontract.JSON)["parameters"].(agentcontract.JSON)
		var native agentcontract.JSON
		for _, tool := range tools {
			function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
			if function["name"] == "submit_final" {
				native = function["parameters"].(agentcontract.JSON)
			}
		}
		for name, schema := range map[string]agentcontract.JSON{"native": native, "unified": unified, "json_schema": nativeDecisionOutputSchema(tools)} {
			for _, fields := range []string{`"answer_json":{}`, `"answer_json":null`, `"answer_markdown":"text"`, `"answer_json":{},"answer_markdown":"text"`, `"result_refs":[]`} {
				raw := `{` + fields + `,"fact_ids":[]`
				if name != "native" {
					raw += `,"kind":"final"`
				}
				if name == "json_schema" {
					raw += `,"schema":"agenstra.decision.v1"`
				}
				if review {
					raw += `,"completion_feedback":""`
				}
				raw += `}`
				valid := strings.Count(fields, `"answer_`) == 1
				if got := validateNativeDecisionOutput([]byte(raw), schema); (got == "") != valid {
					t.Fatalf("%s review=%v fields=%s valid=%v got=%s", name, review, fields, valid, got)
				}
			}
		}
	}
}
