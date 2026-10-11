package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestOutputToolsRejectUndeclaredDecisionNames(t *testing.T) {
	for _, tc := range []struct{ name, arguments string }{
		{"submit_final", `{"answer_markdown":"unpublished","fact_ids":[]}`},
		{"submit_tool_call", `{"capability":"records.write","arguments":{},"reason":"write"}`},
		{"submit_tool_batch", `{"calls":[{"capability":"records.write","arguments":{},"reason":"write"}]}`},
		{"submit_request_input", `{"field":"choice","prompt":"Choose"}`},
		{"submit_search_capabilities", `{"query":"records"}`},
		{"submit_read_skill", `{"name":"guide"}`},
		{"submit_inspect_capability", `{"name":"records.write"}`},
		{"submit_inspect_fact", `{"fact_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","path":[]}`},
		{"inspect_capability", `{"kind":"inspect_capability","name":"records.write"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				assertSingleDecisionOutputRequest(t, payload)
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
					"message":       agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": tc.name, "arguments": tc.arguments}}}},
					"finish_reason": "tool_calls",
				}}, "usage": agentcontract.JSON{"prompt_tokens": 10, "completion_tokens": 5}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("compatible-test", server.URL, "test-key", time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = "output_tools"
			decision, err := model.Decide(t.Context(), agentcontract.ContextPacket{Schema: "agenstra.context.v1", RuntimeFeatures: []string{"capability_search"}, Skills: []agentcontract.JSON{{"name": "guide"}}}, "Choose one decision")
			if agentcontract.ErrorCode(err) != "model_decision_invalid" || decision.Kind != "" || decision.ModelCall == nil || decision.ModelCall.FormatError != "model_output_tool_calls_invalid" || requests != 1 {
				t.Fatalf("undeclared output was accepted or reinterpreted: decision=%+v, err=%v, requests=%d", decision, err, requests)
			}
			if !decision.ModelCall.UsageAvailable || decision.ModelCall.InputTokens != 10 || decision.ModelCall.OutputTokens != 5 {
				t.Fatal("rejected output lost its reported usage", decision.ModelCall)
			}
		})
	}
}

func TestUndeclaredOutputWriteCannotExecuteBeforeBoundedRecovery(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload agentcontract.JSON
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		assertSingleDecisionOutputRequest(t, payload)
		name := "submit_tool_batch"
		arguments := `{"calls":[{"capability":"records.write","arguments":{},"reason":"write"}]}`
		if requests > 1 {
			name, arguments = "submit_decision", `{"kind":"final","answer_markdown":"No business operation was performed.","fact_ids":[]}`
		}
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
			"message": agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": name, "arguments": arguments}}}}, "finish_reason": "tool_calls",
		}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("compatible-test", server.URL, "test-key", time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	model.DecisionOutputMode = "output_tools"
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{"records.write": {Name: "records.write", Effect: "write", InputSchema: agentcontract.JSON{"type": "object"}}}}
	runtime := &reactcore.AgentRuntime{Provider: provider, Model: model}
	result, err := runtime.Run(t.Context(), "Report the result")
	if err != nil || result.Status != "completed" || requests != 2 || provider.called != 0 || len(result.Observations) != 0 || len(result.ModelCalls) != 2 {
		t.Fatalf("undeclared write reached execution: result=%+v, err=%v, requests=%d, calls=%d", result, err, requests, provider.called)
	}
	if result.ModelCalls[0].FormatError != "model_output_tool_calls_invalid" || !result.ModelCalls[1].FormatRecovery || result.ModelUsage.FormatRecoveryRequests != 1 {
		t.Fatal("rejection and bounded recovery lost their accounting", result.ModelCalls, result.ModelUsage)
	}
}
