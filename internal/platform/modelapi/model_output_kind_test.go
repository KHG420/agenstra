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

func TestOutputToolsRejectUndeclaredDecisionKinds(t *testing.T) {
	for _, tc := range []struct {
		name, kind, arguments string
		packet                agentcontract.ContextPacket
		allowed               bool
	}{
		{name: "skill absent", kind: "read_skill", arguments: `{"kind":"read_skill","name":"guide"}`},
		{name: "skill empty", kind: "read_skill", arguments: `{"kind":"read_skill","name":"guide"}`, packet: agentcontract.ContextPacket{Skills: []agentcontract.JSON{}}},
		{name: "skill declared", kind: "read_skill", arguments: `{"kind":"read_skill","name":"guide"}`, packet: agentcontract.ContextPacket{Skills: []agentcontract.JSON{{"name": "guide"}}}, allowed: true},
		{name: "search absent", kind: "search_capabilities", arguments: `{"kind":"search_capabilities","query":"records"}`},
		{name: "search absent from durable features", kind: "search_capabilities", arguments: `{"kind":"search_capabilities","query":"records"}`, packet: agentcontract.ContextPacket{RuntimeFeatures: []string{"durable_execution"}}},
		{name: "search declared", kind: "search_capabilities", arguments: `{"kind":"search_capabilities","query":"records"}`, packet: agentcontract.ContextPacket{RuntimeFeatures: []string{"capability_search"}}, allowed: true},
		{name: "ordinary inspection", kind: "inspect_capability", arguments: `{"kind":"inspect_capability","name":"records.read"}`, allowed: true},
		{name: "review prohibits execution", kind: "tool_batch", arguments: `{"kind":"tool_batch","calls":[{"capability":"records.write","arguments":{},"reason":"write"}]}`, packet: agentcontract.ContextPacket{CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft"}}},
		{name: "review prohibits inspection", kind: "inspect_capability", arguments: `{"kind":"inspect_capability","name":"records.read"}`, packet: agentcontract.ContextPacket{CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft"}}},
		{name: "review final", kind: "final", arguments: `{"kind":"final","answer_markdown":"verified","fact_ids":[],"completion_feedback":""}`, packet: agentcontract.ContextPacket{CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft"}}, allowed: true},
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
				if (outputDecisionSchemas(t, payload)[tc.kind] != nil) != tc.allowed {
					t.Error("fixture does not match the actual declared protocol")
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
					"message": agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": tc.arguments}}}}, "finish_reason": "tool_calls",
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
			tc.packet.Schema = "agenstra.context.v1"
			decision, err := model.Decide(t.Context(), tc.packet, "Choose one declared decision")
			if tc.allowed {
				if err != nil || decision.Kind != tc.kind || decision.ModelCall == nil || decision.ModelCall.FormatError != "" {
					t.Fatal("declared decision was rejected", decision, err)
				}
			} else if agentcontract.ErrorCode(err) != "model_decision_invalid" || decision.Kind != "" || decision.ModelCall == nil || decision.ModelCall.FormatError != "model_decision_schema_invalid" {
				t.Fatal("undeclared decision kind was accepted", decision, err)
			}
			if requests != 1 || decision.ModelCall == nil || !decision.ModelCall.UsageAvailable || decision.ModelCall.InputTokens != 10 || decision.ModelCall.OutputTokens != 5 {
				t.Fatal("decision validation lost usage or retried transport", requests, decision.ModelCall)
			}
		})
	}
}

func TestUndeclaredDecisionKindUsesBoundedRecovery(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload agentcontract.JSON
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if outputDecisionSchemas(t, payload)["search_capabilities"] != nil {
			t.Error("test unexpectedly enabled search")
		}
		arguments := `{"kind":"search_capabilities","query":"records"}`
		if requests > 1 {
			arguments = `{"kind":"final","answer_markdown":"No business operation was performed.","fact_ids":[]}`
		}
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
			"message": agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": arguments}}}}, "finish_reason": "tool_calls",
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
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{"records.read": {Name: "records.read", Effect: "read", InputSchema: agentcontract.JSON{"type": "object"}}}}
	runtime := &reactcore.AgentRuntime{Provider: provider, Model: model}
	result, err := runtime.Run(t.Context(), "Report the result")
	if err != nil || result.Status != "completed" || requests != 2 || provider.called != 0 || len(result.Observations) != 0 || len(result.ModelCalls) != 2 {
		t.Fatal("undeclared kind entered runtime or lost bounded recovery", result, err, requests, provider.called)
	}
	if result.ModelCalls[0].FormatError != "model_decision_schema_invalid" || !result.ModelCalls[1].FormatRecovery || result.ModelUsage.FormatRecoveryRequests != 1 {
		t.Fatal("undeclared kind bypassed format accounting", result.ModelCalls, result.ModelUsage)
	}
}
