package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestOutputToolEnvelopeRecoveryRetainsConcreteConstraint(t *testing.T) {
	for _, failure := range []string{"name", "type", "count"} {
		for _, correct := range []bool{true, false} {
			t.Run(failure+map[bool]string{true: "/corrected", false: "/unchanged"}[correct], func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					var payload agentcontract.JSON
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					assertSingleDecisionOutputRequest(t, payload)
					name, callType, count := "submit_decision", "function", 1
					arguments := `{"kind":"tool_batch","calls":[{"capability":"records.write","arguments":{"secret":"private-rejected-argument"},"reason":"write"}]}`
					switch failure {
					case "name":
						name = "private-rejected-function"
					case "type":
						callType = "private-rejected-type"
					case "count":
						count = 2
					}
					if requests == 2 {
						prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
						_, detail, exists := strings.Cut(prompt, "The rejected output failed its declared parameter schema: ")
						detail, _, _ = strings.Cut(detail, ". Submit corrected parameters")
						located := exists && strings.Contains(detail, "/tool_calls")
						if failure != "count" {
							located = located && strings.Contains(detail, "/"+failure)
						}
						if failure == "name" {
							located = located && strings.Contains(detail, "submit_decision")
						}
						if !located {
							t.Error("tool-envelope recovery omitted its concrete location or declared constraint")
						}
						for _, secret := range []string{"private-rejected-function", "private-rejected-type", "private-rejected-argument", "file://", "schema.json#"} {
							if strings.Contains(prompt, secret) {
								t.Error("tool-envelope recovery exposed rejected data or a local schema URL")
							}
						}
						if located && correct {
							name, callType, count = "submit_decision", "function", 1
							arguments = `{"kind":"final","answer_markdown":"No business operation was performed.","fact_ids":[]}`
						}
					}
					calls := []any{}
					for range count {
						calls = append(calls, agentcontract.JSON{"type": callType, "function": agentcontract.JSON{"name": name, "arguments": arguments}})
					}
					if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
						"message": agentcontract.JSON{"tool_calls": calls}, "finish_reason": "tool_calls",
					}}}); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				model, err := NewHTTPJSONDecisionModel("test", server.URL, "fixture-key", time.Second, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				model.DecisionOutputMode = "output_tools"
				provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{
					"records.write": {Name: "records.write", Effect: "write", ApprovalRequired: true, InputSchema: agentcontract.JSON{"type": "object"}},
				}}
				runtime := &reactcore.AgentRuntime{Provider: provider, Model: model, MaxModelRounds: 4, Grants: map[string]bool{"records.write": true}}
				result, err := runtime.Run(t.Context(), "Report the outcome without executing business operations")
				if err != nil || requests != 2 || len(result.ModelCalls) != 2 || provider.called != 0 || len(result.Observations) != 0 {
					t.Fatal("tool-envelope rejection changed the budget or dispatched a business operation", result, err)
				}
				if result.ModelCalls[0].FormatError != "model_output_tool_calls_invalid" || !result.ModelCalls[1].FormatRecovery || result.ModelUsage.FormatRecoveryRequests != 1 {
					t.Fatal("tool-envelope recovery changed its format accounting")
				}
				if (result.Status == "completed") != correct || !correct && result.Status != "failed" {
					t.Fatal("unchanged tool envelope was accepted or a corrected final was rejected", result)
				}
			})
		}
	}
}

func TestNativeControlGroupingRecoveryRetainsConcreteConstraint(t *testing.T) {
	for _, grouping := range []string{"controls", "control-business", "proposal-business", "final-business", "no-business", "empty"} {
		for _, correct := range []bool{true, false} {
			t.Run(grouping+map[bool]string{true: "/corrected", false: "/unchanged"}[correct], func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					var payload agentcontract.JSON
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					business := ""
					if grouping != "no-business" {
						business = nativeTestFunction(t, payload, "records.read")["name"].(string)
					}
					call := func(name, arguments string) agentcontract.JSON {
						return agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": name, "arguments": arguments}}
					}
					search := call("search_capabilities", `{"query":"private-rejected-argument"}`)
					calls := []any{search, call(business, `{}`)}
					switch grouping {
					case "controls", "no-business":
						calls = []any{search, search}
					case "empty":
						calls = []any{}
					case "proposal-business":
						calls[0] = call("tool_call", `{"capability":"records.read","arguments":{},"reason":"private-rejected-argument"}`)
					case "final-business":
						calls[0] = call("final", `{"answer_markdown":"private-rejected-argument","fact_ids":[]}`)
					}
					if requests == 2 {
						prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
						_, detail, exists := strings.Cut(prompt, "The rejected output failed its declared parameter schema: ")
						detail, _, _ = strings.Cut(detail, ". Submit corrected parameters")
						located := exists && strings.Contains(detail, "/tool_calls")
						if grouping != "no-business" && grouping != "empty" {
							located = located && strings.Contains(detail, "/tool_calls/0/function/name") && strings.Contains(detail, business)
						}
						if !located {
							t.Error("native grouping recovery omitted the rejected control location or permitted business function")
						}
						for _, secret := range []string{"private-rejected-argument", "file://", "schema.json#"} {
							if strings.Contains(prompt, secret) {
								t.Error("native grouping recovery exposed rejected data or a local schema URL")
							}
						}
						if located && correct {
							calls = []any{call("final", `{"answer_markdown":"No business operation was performed.","fact_ids":[]}`)}
						}
					}
					message := agentcontract.JSON{"tool_calls": calls}
					finishReason := "tool_calls"
					if len(calls) == 0 {
						message["content"] = "The requested operation will be executed."
						finishReason = "stop"
					}
					if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
						"message": message, "finish_reason": finishReason,
					}}}); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				model, err := NewHTTPJSONDecisionModel("test", server.URL, "fixture-key", time.Second, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				model.DecisionOutputMode = "native_tools"
				provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{
					"records.read": {Name: "records.read", Effect: "read", InputSchema: agentcontract.JSON{"type": "object"}},
				}}
				runtime := &reactcore.AgentRuntime{Provider: provider, Model: model, MaxModelRounds: 4, MaxContextCapabilities: 8, Grants: map[string]bool{"records.read": true}}
				if grouping == "no-business" {
					runtime.Grants = map[string]bool{}
				}
				result, err := runtime.Run(t.Context(), "Report the outcome without executing business operations")
				if err != nil || requests != 2 || len(result.ModelCalls) != 2 || provider.called != 0 || len(result.Observations) != 0 {
					t.Fatal("native grouping rejection changed the budget or partially dispatched a batch", result, err)
				}
				if result.ModelCalls[0].FormatError != "model_output_tool_calls_invalid" || !result.ModelCalls[1].FormatRecovery || result.ModelUsage.FormatRecoveryRequests != 1 {
					t.Fatal("native grouping recovery changed its format accounting")
				}
				if (result.Status == "completed") != correct || !correct && result.Status != "failed" {
					t.Fatal("unchanged grouping was accepted or a corrected final was rejected", result)
				}
			})
		}
	}
}
