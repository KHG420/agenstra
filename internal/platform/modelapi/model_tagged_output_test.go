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

func assertSingleDecisionOutputRequest(t *testing.T, payload agentcontract.JSON) {
	t.Helper()
	tools, err := agentcontract.CanonicalJSON(payload["tools"])
	if err != nil {
		t.Fatal(err)
	}
	var functions []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(tools, &functions); err != nil || len(functions) != 1 || functions[0].Type != "function" || functions[0].Function.Name != "submit_decision" {
		t.Fatal("request must offer exactly one decision output function", string(tools), err)
	}
	choice, err := agentcontract.CanonicalJSON(payload["tool_choice"])
	if err != nil || string(choice) != `{"function":{"name":"submit_decision"},"type":"function"}` || payload["parallel_tool_calls"] != false || payload["response_format"] != nil {
		t.Fatal("request must explicitly select the single decision function", string(choice), err)
	}
}

func outputDecisionSchemas(t *testing.T, payload agentcontract.JSON) map[string]agentcontract.JSON {
	t.Helper()
	tools := payload["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected one decision output function, got %d", len(tools))
	}
	function := tools[0].(agentcontract.JSON)["function"].(agentcontract.JSON)
	if function["name"] != "submit_decision" {
		t.Fatalf("unexpected output function: %v", function["name"])
	}
	parameters := function["parameters"].(agentcontract.JSON)
	variants := parameters["anyOf"].([]any)
	result := map[string]agentcontract.JSON{}
	for _, raw := range variants {
		schema := raw.(agentcontract.JSON)
		kind := schema["properties"].(agentcontract.JSON)["kind"].(agentcontract.JSON)["const"].(string)
		if result[kind] != nil {
			t.Fatalf("duplicate decision kind: %s", kind)
		}
		result[kind] = schema
	}
	return result
}

func TestTaggedOutputRequestOffersOneFunctionWithAvailableDecisions(t *testing.T) {
	m := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: "output_tools"}
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", RuntimeFeatures: []string{"capability_search"}, Skills: []agentcontract.JSON{{"name": "guide"}}}
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := m.requestPayload(raw, "Choose one decision")
	if err != nil {
		t.Fatal(err)
	}
	schemas := outputDecisionSchemas(t, payload)
	if len(schemas) != 7 || schemas["tool_batch"] == nil || schemas["search_capabilities"] == nil || schemas["read_skill"] == nil {
		t.Fatalf("available decision kinds changed: %v", schemas)
	}
	choice := payload["tool_choice"].(agentcontract.JSON)
	if choice["type"] != "function" || choice["function"].(agentcontract.JSON)["name"] != "submit_decision" || payload["parallel_tool_calls"] != false {
		t.Fatal("single output selection is not enforced")
	}
}

func TestTaggedOutputBoundary(t *testing.T) {
	cases := []struct {
		name, arguments, want string
	}{
		{"final", `{"kind":"final","answer_markdown":"verified","fact_ids":[]}`, ""},
		{"batch", `{"kind":"tool_batch","calls":[{"capability":"records.read","arguments":{"id":23},"reason":"read"}]}`, ""},
		{"wrapped decision", `{"decision":{"kind":"final","answer_markdown":"verified","fact_ids":[]}}`, "model_decision_invalid"},
		{"extra wrapper field", `{"decision":{"kind":"final","answer_markdown":"verified","fact_ids":[]},"execute":true}`, "model_decision_invalid"},
		{"missing kind", `{"answer_markdown":"verified","fact_ids":[]}`, "model_decision_invalid"},
		{"unknown kind", `{"kind":"execute","calls":[]}`, "model_decision_invalid"},
		{"schema override", `{"kind":"final","schema":"other","answer_markdown":"verified","fact_ids":[]}`, "model_decision_invalid"},
		{"extra decision field", `{"kind":"final","answer_markdown":"verified","fact_ids":[],"execute":true}`, "model_decision_invalid"},
		{"stringified decision", `"{\"kind\":\"final\"}"`, "model_decision_invalid"},
		{"duplicate kind", `{"kind":"final","kind":"tool_batch","answer_markdown":"verified","fact_ids":[]}`, "model_decision_invalid"},
		{"invalid call reference", `{"kind":"tool_batch","calls":[{"call_ref":"Bad-1","capability":"records.read","arguments":{},"reason":"read"}]}`, "model_decision_invalid"},
		{"mixed decision fields", `{"kind":"final","answer_markdown":"verified","fact_ids":[],"calls":[]}`, "model_decision_invalid"},
		{"stringified calls", `{"kind":"tool_batch","calls":"[]"}`, "model_decision_invalid"},
		{"stringified fact ids", `{"kind":"final","answer_markdown":"verified","fact_ids":"[]"}`, "model_decision_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": tc.arguments}}}}, "finish_reason": "tool_calls"}}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			m, err := NewHTTPJSONDecisionModel("compatible-model", upstream.URL, "test-key", time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			m.DecisionOutputMode = "output_tools"
			decision, err := m.Decide(context.Background(), agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "Choose one decision")
			if got := agentcontract.ErrorCode(err); got != tc.want {
				t.Fatalf("error=%s, want=%s", got, tc.want)
			}
			if tc.name == "batch" {
				if len(decision.Calls) != 1 || !agentcontract.CallRefPattern.MatchString(decision.Calls[0].CallRef) {
					t.Fatal("fresh local identity changed", decision)
				}
				arguments, err := agentcontract.CanonicalJSON(decision.Calls[0].Arguments)
				if err != nil || string(arguments) != `{"id":23}` {
					t.Fatal("business arguments changed", decision, err)
				}
			}
		})
	}
}

func TestTaggedOutputRejectsWholeMultipleDecisionResponseBeforeExecution(t *testing.T) {
	for _, second := range []string{
		`{"kind":"tool_batch","calls":[{"capability":"records.write","arguments":{"id":23},"reason":"write"}]}`,
		`{"kind":"inspect_fact","fact_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","path":[]}`,
	} {
		t.Run(second, func(t *testing.T) {
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				assertSingleDecisionOutputRequest(t, payload)
				arguments := []string{`{"kind":"tool_batch","calls":[{"capability":"records.write","arguments":{"id":23},"reason":"write"}]}`, second}
				if requests == 2 {
					arguments = []string{`{"kind":"final","answer_markdown":"No business operation was performed.","fact_ids":[]}`}
				}
				calls := []any{}
				for _, argument := range arguments {
					calls = append(calls, agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": argument}})
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"tool_calls": calls}, "finish_reason": "tool_calls"}}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			model, err := NewHTTPJSONDecisionModel("compatible-model", upstream.URL, "test-key", time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = "output_tools"
			provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
			runtime := &reactcore.AgentRuntime{Provider: provider, Model: model}
			result, err := runtime.Run(t.Context(), "Report the result")
			if err != nil || result.Status != "completed" || requests != 2 || provider.called != 0 || len(result.Observations) != 0 || len(result.ModelCalls) != 2 {
				t.Fatalf("result=%+v requests=%d invoked=%d err=%v", result, requests, provider.called, err)
			}
			if result.ModelCalls[0].FormatError != "model_output_tool_calls_invalid" || !result.ModelCalls[1].FormatRecovery || result.ModelUsage.FormatRecoveryRequests != 1 {
				t.Fatal("invalid response or its bounded recovery was not accounted for", result.ModelCalls, result.ModelUsage)
			}
		})
	}
}

func TestTaggedOutputRequestDeclaresNativeRootPropertyTypes(t *testing.T) {
	model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: "output_tools"}
	payload, err := model.requestPayload([]byte(`{"schema":"agenstra.context.v1"}`), "Choose one decision")
	if err != nil {
		t.Fatal(err)
	}
	assertSingleDecisionOutputRequest(t, payload)
	function := payload["tools"].([]any)[0].(agentcontract.JSON)["function"].(agentcontract.JSON)
	parameters := function["parameters"].(agentcontract.JSON)
	properties := parameters["properties"].(agentcontract.JSON)
	for field, typ := range map[string]string{"kind": "string", "calls": "array", "fact_ids": "array", "answer_markdown": "string", "result_refs": "array", "path": "array", "input_schema": ""} {
		schema, ok := properties[field].(agentcontract.JSON)
		if !ok || (typ != "" && schema["type"] != typ) {
			t.Fatalf("native property type missing: %s", field)
		}
	}
	if parameters["type"] != "object" || properties["decision"] != nil || len(parameters["anyOf"].([]any)) == 0 {
		t.Fatal("flat typed decision union changed")
	}
	prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
	if strings.Contains(prompt, "Do not call historical output functions") || !strings.Contains(prompt, "Saved calls are historical evidence") {
		t.Fatal("current output function conflicts with historical evidence guidance")
	}
}
