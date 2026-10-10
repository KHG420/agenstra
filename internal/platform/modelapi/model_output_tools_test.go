package modelapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
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
		{"stringified batch", "submit_tool_batch", `{"calls":"[{\"call_ref\":\"read-1\",\"capability\":\"x\",\"arguments\":{},\"reason\":\"read\"}]"}`, "", "tool_calls", "model_decision_invalid", 1},
		{"single bad ref", "submit_tool_call", `{"call_ref":"Get-1","capability":"x","arguments":{},"reason":"read"}`, "", "tool_calls", "model_decision_invalid", 1},
		{"single stringified arguments", "submit_tool_call", `{"call_ref":"read-1","capability":"x","arguments":"{}","reason":"read"}`, "", "tool_calls", "model_decision_invalid", 1},
		{"single unknown field", "submit_tool_call", `{"call_ref":"read-1","capability":"x","arguments":{},"reason":"read","execute":true}`, "", "tool_calls", "model_decision_invalid", 1},
		{"single schema override", "submit_tool_call", `{"call_ref":"read-1","capability":"x","arguments":{},"reason":"read","schema":"agenstra.decision.v1"}`, "", "tool_calls", "model_decision_invalid", 1},
		{"single kind override", "submit_tool_call", `{"call_ref":"read-1","capability":"x","arguments":{},"reason":"read","kind":"final"}`, "", "tool_calls", "model_decision_invalid", 1},
		{"single duplicate key", "submit_tool_call", `{"call_ref":"read-1","capability":"x","arguments":{},"reason":"read","reason":"write"}`, "", "tool_calls", "model_decision_invalid", 1},
		{"single batch wrapper", "submit_tool_call", `{"calls":[{"call_ref":"read-1","capability":"x","arguments":{},"reason":"read"}]}`, "", "tool_calls", "model_decision_invalid", 1},
		{"single multiple outputs", "submit_tool_call", `{"call_ref":"read-1","capability":"x","arguments":{},"reason":"read"}`, "", "tool_calls", "model_decision_invalid", 2},
		{"single truncated", "submit_tool_call", `{"call_ref":"read-1","capability":"x","arguments":{},"reason":"read"}`, "", "length", "model_output_truncated", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["response_format"] != nil || payload["tool_choice"] != "required" || len(payload["tools"].([]any)) != 6 {
					t.Error("incorrect output tool request")
				}
				calls := []any{}
				for range tc.count {
					calls = append(calls, agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": tc.tool, "arguments": tc.arguments}})
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": tc.content, "tool_calls": calls}, "finish_reason": tc.finish}}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			m, err := NewHTTPJSONDecisionModel("hy3", upstream.URL, "test-key", time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			m.DecisionOutputMode = "output_tools"
			decision, err := m.Decide(context.Background(), agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "Return exactly one raw JSON object.")
			if got := agentcontract.ErrorCode(err); got != tc.want {
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

func TestDecisionOutputToolsPreserveSingleAndBatchCalls(t *testing.T) {
	call := `{"call_ref":"read-1","capability":"pack.read","arguments":{"operation":"GetByID","arguments":{"id":2,"values":[true,"line 1\nline 2",null]}},"reason":"Read the requested object"}`
	for _, name := range []string{"submit_tool_call", "submit_tool_batch"} {
		t.Run(name, func(t *testing.T) {
			arguments := call
			if name == "submit_tool_batch" {
				arguments = `{"calls":[` + call + `,` + strings.Replace(call, "read-1", "read-2", 1) + `]}`
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
					"message":       agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": name, "arguments": arguments}}}},
					"finish_reason": "tool_calls",
				}}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			m, err := NewHTTPJSONDecisionModel("compatible-test-model", upstream.URL, "test-key", time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			m.DecisionOutputMode = "output_tools"
			decision, err := m.Decide(context.Background(), agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "")
			if err != nil {
				t.Fatal(err)
			}
			wantBody := `{"schema":"agenstra.decision.v1","kind":"tool_batch","calls":[` + call + `]}`
			if name == "submit_tool_batch" {
				wantBody = `{"schema":"agenstra.decision.v1","kind":"tool_batch","calls":[` + call + `,` + strings.Replace(call, "read-1", "read-2", 1) + `]}`
			}
			want, err := agentcontract.StrictDecision([]byte(wantBody))
			if err != nil {
				t.Fatal(err)
			}
			decision.ModelCall = nil
			for i := range decision.Calls {
				if !agentcontract.CallRefPattern.MatchString(decision.Calls[i].CallRef) || decision.Calls[i].CallRef == want.Calls[i].CallRef {
					t.Fatal("adapter did not assign a fresh local reference", decision.Calls[i].CallRef)
				}
				decision.Calls[i].CallRef = want.Calls[i].CallRef
			}
			gotJSON, err := agentcontract.CanonicalJSON(decision)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := agentcontract.CanonicalJSON(want)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestDecisionOutputToolsAssignFreshCallReferences(t *testing.T) {
	for _, tc := range []struct{ name, tool, arguments string }{
		{"single without reference", "submit_tool_call", `{"capability":"pack.read","arguments":{"id":2},"reason":"Read"}`},
		{"batch without references", "submit_tool_batch", `{"calls":[{"capability":"pack.read","arguments":{"id":2},"reason":"Read"},{"capability":"pack.read","arguments":{"id":3},"reason":"Read"}]}`},
		{"repeated supplied reference", "submit_tool_call", `{"call_ref":"read-1","capability":"pack.read","arguments":{"id":2},"reason":"Read"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
					"message":       agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": tc.tool, "arguments": tc.arguments}}}},
					"finish_reason": "tool_calls",
				}}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			model, err := NewHTTPJSONDecisionModel("compatible-test-model", upstream.URL, "test-key", time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = "output_tools"
			seen := map[string]bool{}
			for range 2 {
				decision, err := model.Decide(t.Context(), agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "")
				if err != nil {
					t.Fatal(err)
				}
				for _, call := range decision.Calls {
					if !agentcontract.CallRefPattern.MatchString(call.CallRef) || seen[call.CallRef] || call.CallRef == "read-1" {
						t.Fatalf("reference must be valid and assigned once by the adapter: %q", call.CallRef)
					}
					seen[call.CallRef] = true
				}
			}
		})
	}
}

func TestDecisionOutputToolsMeasurementAndMemoryIsolation(t *testing.T) {
	m := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "output_tools", MaxOutputTokens: 16384}
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := m.requestPayload(raw, "Return exactly one raw JSON object.")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range payload["tools"].([]any) {
		parameters := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)["parameters"].(agentcontract.JSON)
		if parameters["properties"] == nil || parameters["oneOf"] != nil {
			t.Fatal("missing top-level property types", parameters)
		}
	}
	wire, err := agentcontract.CanonicalJSON(payload)
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
	if _, err = m.requestPayload(raw, ""); agentcontract.ErrorCode(err) != "model_parameters_invalid" {
		t.Fatal("invalid mode accepted", err)
	}
}

func TestDecisionOutputToolsOmitUnavailableSkill(t *testing.T) {
	for _, tc := range []struct {
		name   string
		skills []agentcontract.JSON
	}{
		{name: "no declared skills"},
		{name: "declared skill", skills: []agentcontract.JSON{{"name": "pack.guide", "description": "Usage instructions"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "output_tools"}
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Skills: tc.skills}
			raw, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := model.requestPayload(raw, "Choose one JSON decision at a time.")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range payload["tools"].([]any) {
				if tool.(agentcontract.JSON)["function"].(agentcontract.JSON)["name"] == "submit_read_skill" {
					found = true
				}
			}
			want := len(tc.skills) > 0
			prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
			if found != want || strings.Contains(prompt, "submit_read_skill: required") != want {
				t.Fatalf("skill selection must match declared availability: tool=%v, want=%v", found, want)
			}
		})
	}
}

func TestDecisionOutputToolsRecoveryRejectsMultipleInspectionsBeforeExecution(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload agentcontract.JSON
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		function := agentcontract.JSON{"name": "submit_inspect_fact", "arguments": `{"fact_id":"12345678-1234-1234-1234-123456789012","path":["data"]}`}
		calls := []any{agentcontract.JSON{"type": "function", "function": function}, agentcontract.JSON{"type": "function", "function": function}}
		if requests == 2 {
			prompt := payload["messages"].([]any)[0].(map[string]any)["content"].(string)
			if !strings.Contains(prompt, "response did not contain exactly one valid registered output tool") || !strings.Contains(prompt, "never return several output functions") {
				t.Error("missing actionable output-count feedback", prompt)
			}
			calls = []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_final", "arguments": `{"answer_markdown":"hello","fact_ids":[]}`}}}
		}
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"tool_calls": calls}, "finish_reason": "tool_calls"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	m, err := NewHTTPJSONDecisionModel("compatible-test-model", upstream.URL, "test-key", time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.DecisionOutputMode = "output_tools"
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
	runtime := &reactcore.AgentRuntime{Provider: provider, Model: m}
	result, err := runtime.Run(t.Context(), "hello")
	if err != nil || result.Status != "completed" || requests != 2 || provider.called != 0 || len(result.Observations) != 0 || len(result.ModelCalls) != 2 {
		t.Fatalf("result=%+v requests=%d invoked=%d err=%v", result, requests, provider.called, err)
	}
	if result.ModelCalls[0].FormatError != "model_output_tool_calls_invalid" || !result.ModelCalls[1].FormatRecovery || result.ModelUsage.FormatRecoveryRequests != 1 {
		t.Fatal("output-count failure or recovery not accounted for", result.ModelCalls, result.ModelUsage)
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
	prompt := "Your previous review was invalid. Return exactly one final agenstra.decision.v1 JSON decision; no tools or other decision kinds are allowed during completion review.\nYour previous decision was invalid: tool_batch.calls has at most 4 items. Return one valid agenstra.decision.v1 JSON decision with no more than 4 calls.\nYour previous response was not a valid agenstra.decision.v1 JSON decision. Return exactly one raw JSON object without Markdown or code fences. Preserve the capability's input structure.\n" + agentcontract.DecisionObjectShapePrompt
	prompt += "\nYour previous response reached the output token limit. Return one concise, complete agenstra.decision.v1 JSON decision within the same output budget. Never repeat a successful write. This is completion review: return only a final decision; tools and other decision kinds are not allowed."
	got := decisionOutputToolPrompt(prompt)
	if strings.Contains(got, "JSON decision") || strings.Contains(got, "Return exactly one raw JSON") || strings.Contains(got, agentcontract.DecisionObjectShapePrompt) || strings.Contains(got, "tools and other decision kinds are not allowed") || !strings.Contains(got, "no more than 4 calls") || !strings.Contains(got, "no capability calls or other decision kinds") || !strings.Contains(got, "Preserve the capability's input structure") || !strings.Contains(got, "adapter supplies a fresh local reference") || !strings.Contains(got, "capability calls and other decision kinds are not allowed") || !strings.Contains(got, "Never repeat a successful write") {
		t.Fatal(got)
	}
}
