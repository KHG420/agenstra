package agenstra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecisionOutputToolsRejectMalformedOutput(t *testing.T) {
	valid := `{"answer_markdown":"line 1\nline 2","fact_ids":[]}`
	cases := []struct {
		name, tool, arguments, content, finish, want string
		count                                        int
	}{
		{"final", "submit_final", valid, "ignored non-executable commentary", "tool_calls", "", 1},
		{"no output tool", "", "", valid, "stop", "model_decision_invalid", 0},
		{"multiple", "submit_final", valid, "", "tool_calls", "model_decision_invalid", 2},
		{"unknown tool", "submit_other", valid, "", "tool_calls", "model_decision_invalid", 1},
		{"kind mismatch", "submit_tool_batch", valid, "", "tool_calls", "model_decision_invalid", 1},
		{"stringified ids", "submit_final", `{"answer_markdown":"ok","fact_ids":"[]"}`, "", "tool_calls", "model_decision_invalid", 1},
		{"duplicate keys", "submit_final", `{"answer_markdown":"ok","fact_ids":[],"fact_ids":[]}`, "", "tool_calls", "model_decision_invalid", 1},
		{"truncated", "submit_final", valid, "", "length", "model_output_truncated", 1},
		{"unknown field", "submit_final", `{"answer_markdown":"ok","fact_ids":[],"execute":true}`, "", "tool_calls", "model_decision_invalid", 1},
		{"bad ref", "submit_tool_batch", `{"calls":[{"call_ref":"Get-1","capability":"x","arguments":{},"reason":"read"}]}`, "", "tool_calls", "model_decision_invalid", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["response_format"] != nil || payload["tool_choice"] != "required" || len(payload["tools"].([]any)) != 7 {
					t.Error("incorrect output tool request")
				}
				calls := []any{}
				for range tc.count {
					calls = append(calls, JSON{"type": "function", "function": JSON{"name": tc.tool, "arguments": tc.arguments}})
				}
				if err := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": tc.content, "tool_calls": calls}, "finish_reason": tc.finish}}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			m, err := NewHTTPJSONDecisionModel("hy3", upstream.URL, "test-key", time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			m.DecisionOutputMode = "output_tools"
			decision, err := m.Decide(context.Background(), ContextPacket{Schema: "agenstra.context.v1"}, "Return exactly one raw JSON object.")
			if got := ErrorCode(err); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if tc.want == "" && decision.AnswerMarkdown != "line 1\nline 2" {
				t.Fatal("changed string data", decision)
			}
			if requests != 1 {
				t.Fatalf("unexpected retry: %d", requests)
			}
		})
	}
}

func TestDecisionOutputToolsMeasurementAndMemoryIsolation(t *testing.T) {
	m := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "output_tools", MaxOutputTokens: 16384}
	packet := ContextPacket{Schema: "agenstra.context.v1"}
	raw, err := CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := m.requestPayload(raw, "Return exactly one raw JSON object.")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range payload["tools"].([]any) {
		parameters := tool.(JSON)["function"].(JSON)["parameters"].(JSON)
		if parameters["properties"] == nil || parameters["oneOf"] != nil {
			t.Fatal("missing top-level property types", parameters)
		}
	}
	wire, err := CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	measured := false
	m.CountInputTokens = func(_ string, body []byte) (int64, error) {
		measured = true
		if string(body) != string(wire) {
			t.Fatal("measurement does not include exact tool request")
		}
		return 123, nil
	}
	size, err := m.MeasureInput(packet, "Return exactly one raw JSON object.")
	if err != nil || size.Tokens != 123 || !measured {
		t.Fatal(size, err)
	}
	memory, err := m.requestPayload([]byte(`{"schema":"agenstra.memory_input.v1","messages":[]}`), "Return proposals as JSON")
	if err != nil || memory["tools"] != nil || memory["response_format"] == nil {
		t.Fatal("memory output changed", memory, err)
	}
	m.DecisionOutputMode = ""
	legacy, err := m.requestPayload(raw, "Return exactly one raw JSON object.")
	if err != nil || legacy["tools"] != nil || legacy["response_format"] == nil {
		t.Fatal("default changed", legacy, err)
	}
	m.DecisionOutputMode = "invalid"
	if _, err = m.requestPayload(raw, ""); ErrorCode(err) != "model_parameters_invalid" {
		t.Fatal("invalid mode accepted", err)
	}
}

func TestDecisionOutputToolPromptRetainsRuntimeRules(t *testing.T) {
	prompt := "Keep authorization and evidence.\nReturn one JSON object with schema agenstra.decision.v1. Tool example: {}.\nSkill: {}\nReturn exactly one raw JSON object. Do not wrap it in Markdown."
	got := decisionOutputToolPrompt(prompt)
	if !strings.Contains(got, "Keep authorization and evidence.") || strings.Contains(got, "Return exactly one raw JSON") || !strings.Contains(got, "runtime will separately validate and authorize") {
		t.Fatal(got)
	}
}

func TestDecisionOutputToolsRecoveryKeepsConstraintsWithoutConflictingFormat(t *testing.T) {
	prompt := "Your previous review was invalid. Return exactly one final agenstra.decision.v1 JSON decision; no tools or other decision kinds are allowed during completion review.\nYour previous decision was invalid: tool_batch.calls has at most 4 items. Return one valid agenstra.decision.v1 JSON decision with no more than 4 calls.\nYour previous response was not a valid agenstra.decision.v1 JSON decision. Return exactly one raw JSON object without Markdown or code fences. Preserve the capability's input structure."
	got := decisionOutputToolPrompt(prompt)
	if strings.Contains(got, "JSON decision") || strings.Contains(got, "Return exactly one raw JSON") || !strings.Contains(got, "no more than 4 calls") || !strings.Contains(got, "no tools or other decision kinds") || !strings.Contains(got, "Preserve the capability's input structure") {
		t.Fatal(got)
	}
}
