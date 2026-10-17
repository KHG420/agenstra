package modelapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestNativeMultipleBusinessCallsBecomeOneBoundedDecision(t *testing.T) {
	for _, count := range []int{2, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			packet := nativeTestPacket()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if payload["parallel_tool_calls"] != false {
					t.Error("changed the existing provider preference")
				}
				function := nativeTestFunction(t, payload, "inventory.get")
				calls := []any{}
				for i := 0; i < count; i++ {
					calls = append(calls, agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": function["name"], "arguments": fmt.Sprintf(`{"id":%d}`, i+1)}})
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"tool_calls": calls}, "finish_reason": "tool_calls"}}, "usage": agentcontract.JSON{"prompt_tokens": 10, "completion_tokens": 20}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("compatible", server.URL, "test", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = "native_tools"
			decision, err := model.Decide(t.Context(), packet, "Read independent inventory records")
			if err != nil || decision.Kind != "tool_batch" || len(decision.Calls) != count || requests != 1 {
				t.Fatalf("legitimate native business batch rejected: %+v %v requests=%d", decision, err, requests)
			}
			seen := map[string]bool{}
			for i, call := range decision.Calls {
				raw, err := agentcontract.CanonicalJSON(call.Arguments)
				if err != nil || string(raw) != fmt.Sprintf(`{"id":%d}`, i+1) || call.Capability != "inventory.get" || !agentcontract.CallRefPattern.MatchString(call.CallRef) || seen[call.CallRef] {
					t.Fatal("native order, arguments, binding or fresh identity changed", call, err)
				}
				seen[call.CallRef] = true
			}
			if decision.ModelCall.FormatError != "" || !decision.ModelCall.UsageAvailable || decision.ModelCall.InputTokens != 10 || decision.ModelCall.OutputTokens != 20 {
				t.Fatal("valid batch lost usage or was counted as format recovery", decision.ModelCall)
			}
		})
	}
}

func TestNativeMultipleCallsKeepStrictWholeResponseRejection(t *testing.T) {
	for _, tc := range []struct {
		name, second, secondArguments, want string
		count                               int
		output                              int
	}{
		{name: "unknown second name", second: "unknown_function", secondArguments: `{}`, count: 2, want: "model_decision_invalid"},
		{name: "mixed final", second: "final", secondArguments: `{"answer_markdown":"unpublished","fact_ids":[]}`, count: 2, want: "model_decision_invalid"},
		{name: "mixed proposal", second: "tool_call", secondArguments: `{"capability":"inventory.get","arguments":{"id":8},"reason":"Read"}`, count: 2, want: "model_decision_invalid"},
		{name: "second numeric string", second: "business", secondArguments: `{"id":"private-value"}`, count: 2, want: "model_decision_invalid"},
		{name: "second duplicate key", second: "business", secondArguments: `{"id":8,"id":9}`, count: 2, want: "model_decision_invalid"},
		{name: "second syntax error", second: "business", secondArguments: `{"id":}`, count: 2, want: "model_decision_invalid"},
		{name: "second cut off at cap", second: "business", secondArguments: `{"id":`, count: 2, output: 4096, want: "model_output_truncated"},
		{name: "five business calls", second: "business", secondArguments: `{"id":8}`, count: 5, want: "model_decision_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				function := nativeTestFunction(t, payload, "inventory.get")
				name := tc.second
				if name == "business" {
					name = function["name"].(string)
				}
				calls := []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": function["name"], "arguments": `{"id":7}`}}}
				for i := 1; i < tc.count; i++ {
					calls = append(calls, agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": name, "arguments": tc.secondArguments}})
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"tool_calls": calls}, "finish_reason": "tool_calls"}}, "usage": agentcontract.JSON{"prompt_tokens": 10, "completion_tokens": tc.output}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("compatible", server.URL, "test", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode, model.MaxOutputTokens = "native_tools", 4096
			decision, err := model.Decide(t.Context(), nativeTestPacket(), "Read independent records")
			if agentcontract.ModelErrorCode(err) != tc.want || len(decision.Calls) != 0 || requests != 1 || strings.Contains(fmt.Sprint(err), "private-value") {
				t.Fatalf("invalid later call was repaired, partially accepted or lost its failure: %+v %v requests=%d", decision, err, requests)
			}
			if tc.want == "model_output_truncated" && decision.ModelCall.FormatError != "" {
				t.Fatal("truncated second arguments were relabelled as ordinary invalid JSON", decision.ModelCall)
			}
			if tc.name == "second syntax error" && !strings.Contains(err.Error(), "invalid character") {
				t.Fatal("second call syntax cause lost", err)
			}
		})
	}
}

func TestNativeBusinessBatchRetainsRuntimeApprovalAndAccounting(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload agentcontract.JSON
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		calls := []any{}
		for _, name := range []string{"records.read", "records.write"} {
			function := nativeTestFunction(t, payload, name)
			calls = append(calls, agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": function["name"], "arguments": `{"id":7}`}})
		}
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"tool_calls": calls}, "finish_reason": "tool_calls"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("compatible", server.URL, "test", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.DecisionOutputMode = "native_tools"
	schema := nativeTestPacket().Capabilities[0]["input_schema"].(agentcontract.JSON)
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{
		"records.read":  {Name: "records.read", Effect: "read", InputSchema: schema},
		"records.write": {Name: "records.write", Effect: "write", ApprovalRequired: true, InputSchema: schema},
	}}
	runtime := &reactcore.AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"records.read": true, "records.write": true}, MaxModelRounds: 4, MaxToolCalls: 4}
	result, err := runtime.Run(t.Context(), "Read and perform the independently authorized write, preserving required approval")
	if err != nil || result.Status != "needs_approval" || requests != 1 || provider.called != 0 || len(result.Facts) != 0 || len(result.Observations) != 0 || len(result.Decisions) != 1 || len(result.ModelCalls) != 1 || result.ModelUsage.FormatRecoveryRequests != 0 {
		t.Fatal("native batching bypassed approval, executed a partial batch or consumed a recovery", result, err, requests, provider.called)
	}
}
