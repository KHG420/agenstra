package deployment

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestModelCheckUsesSelectedOutputProtocol(t *testing.T) {
	final := `{"kind":"final","answer_markdown":"ok","fact_ids":[]}`
	for _, tc := range []struct {
		name, mode, purpose, content, tool, arguments, want string
	}{
		{"json decision", "json_object", "decision", final, "", "", ""},
		{"json review", "json_object", "completion_review", `{"kind":"approve_completion"}`, "", "", ""},
		{"schema review", "json_schema", "completion_review", `{"schema":"agenstra.decision.v1","kind":"approve_completion"}`, "", "", ""},
		{"typed review", "output_tools", "completion_review", "", "submit_decision", `{"kind":"approve_completion"}`, ""},
		{"native review", "native_tools", "completion_review", "", "approve_completion", `{}`, ""},
		{"review final requires feedback", "json_object", "completion_review", final, "", "", "model_decision_schema_invalid"},
		{"native schema decision", "json_schema", "decision", `{"schema":"agenstra.decision.v1","kind":"final","answer_markdown":"ok","fact_ids":[]}`, "", "", ""},
		{"native schema missing identity", "json_schema", "decision", final, "", "", "model_decision_schema_invalid"},
		{"typed decision", "output_tools", "decision", "", "submit_decision", final, ""},
		{"plain JSON cannot prove typed support", "output_tools", "decision", final, "", "", "model_output_tool_calls_invalid"},
		{"undeclared typed output", "output_tools", "decision", final, "submit_final", `{"answer_markdown":"ok","fact_ids":[]}`, "model_output_tool_calls_invalid"},
		{"typed malformed arguments", "output_tools", "decision", "", "submit_decision", `{"kind":"final","answer_markdown":"ok","fact_ids":"[]"}`, "model_decision_schema_invalid"},
		{"memory retains JSON", "output_tools", "memory_extraction", `{"proposals":[]}`, "", "", ""},
		{"native profile memory retains JSON", "json_schema", "memory_extraction", `{"proposals":[]}`, "", "", ""},
		{"native function decision", "native_tools", "decision", "", "final", `{"answer_markdown":"ok","fact_ids":[]}`, ""},
		{"plain JSON cannot prove native functions", "native_tools", "decision", final, "", "", "model_output_tool_calls_invalid"},
		{"native wrapper rejected", "native_tools", "decision", "", "final", final, "model_decision_schema_invalid"},
		{"native function memory retains JSON", "native_tools", "memory_extraction", `{"proposals":[]}`, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				typed := tc.mode == "output_tools" && tc.purpose != "memory_extraction"
				if typed {
					tools, err := agentcontract.CanonicalJSON(payload["tools"])
					var functions []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					}
					if err != nil || json.Unmarshal(tools, &functions) != nil || len(functions) != 1 || functions[0].Function.Name != "submit_decision" {
						t.Error("check did not declare the selected output function", string(tools), err)
					}
					choice, err := agentcontract.CanonicalJSON(payload["tool_choice"])
					if err != nil || string(choice) != `"auto"` || payload["parallel_tool_calls"] != false || payload["response_format"] != nil {
						t.Error("check changed selected output transport", string(choice), err)
					}
				} else if tc.mode == "native_tools" && tc.purpose != "memory_extraction" {
					var functions []struct{ Function struct{ Name string } }
					raw, err := agentcontract.CanonicalJSON(payload["tools"])
					if err != nil || json.Unmarshal(raw, &functions) != nil || len(functions) == 0 {
						t.Error("native check omitted its function definitions", err)
					}
					finalDeclared := false
					for _, function := range functions {
						finalDeclared = finalDeclared || function.Function.Name == "final"
						if function.Function.Name == "submit_decision" {
							t.Error("native check declared the wrong transport")
						}
					}
					if !finalDeclared || payload["tool_choice"] != "auto" || payload["parallel_tool_calls"] != false || payload["response_format"] != nil {
						t.Error("native check changed output channel")
					}
				} else {
					format := "json_object"
					if tc.mode == "json_schema" && tc.purpose != "memory_extraction" {
						format = "json_schema"
					}
					if payload["tools"] != nil || payload["tool_choice"] != nil || payload["response_format"].(map[string]any)["type"] != format {
						t.Error("JSON or memory check changed output channel", payload)
					}
				}
				message := agentcontract.JSON{"content": tc.content}
				if tc.tool != "" {
					message["tool_calls"] = []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": tc.tool, "arguments": tc.arguments}}}
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"usage": agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 10}, "choices": []any{agentcontract.JSON{"message": message}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			manager, err := selectionTestDeployment(t, server.URL).NewModel()
			if err != nil {
				t.Fatal(err)
			}
			config := manager.Snapshot().Config
			profile := config.Profiles["business"]
			profile.DecisionOutputMode, profile.MaxAttempts = tc.mode, 3
			config.Profiles["business"] = profile
			result, err := manager.Check(t.Context(), "business", tc.purpose, &config)
			if err != nil || result.Passed != (tc.want == "") || result.ErrorCode != tc.want || requests != 1 || result.Metrics.Attempts != 1 || result.Metrics.Purpose != tc.purpose || !result.Metrics.UsageAvailable {
				t.Fatalf("check=%+v err=%v requests=%d", result, err, requests)
			}
			if manager.Snapshot().Revision != 0 || manager.Snapshot().Config.Profiles["business"].DecisionOutputMode != "" {
				t.Fatal("protocol check mutated selected configuration")
			}
		})
	}
}
