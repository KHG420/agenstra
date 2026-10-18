package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestNativeSchemaRecoveryRetainsReferencedTypes(t *testing.T) {
	for _, target := range []string{"object", "array item"} {
		for _, corrected := range []bool{true, false} {
			name := target + map[bool]string{true: "/corrected", false: "/unchanged"}[corrected]
			t.Run(name, func(t *testing.T) {
				input := numericArrayInput()
				body := input["properties"].(agentcontract.JSON)["body"].(agentcontract.JSON)
				invalid := `{"body":"{\"ids\":[\"private-rejected-value\"]}"}`
				locations, expected := []string{"/body"}, "object"
				if target == "object" {
					input["$defs"] = agentcontract.JSON{"Body": body}
					input["properties"].(agentcontract.JSON)["body"] = agentcontract.JSON{"$ref": "#/$defs/Body"}
				} else {
					input["$defs"] = agentcontract.JSON{"ID": agentcontract.JSON{"type": "integer"}}
					body["properties"].(agentcontract.JSON)["ids"].(agentcontract.JSON)["items"] = agentcontract.JSON{"$ref": "#/$defs/ID"}
					invalid = `{"body":{"ids":["private-rejected-value",64,"private-rejected-value"]}}`
					locations, expected = []string{"/body/ids/0", "/body/ids/2"}, "integer"
				}

				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := requests.Add(1)
					var payload agentcontract.JSON
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					function := nativeTestFunction(t, payload, "records.reorder")
					arguments := invalid
					if n == 2 {
						prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
						_, feedback, hasFeedback := strings.Cut(prompt, "The rejected output failed its declared parameter schema: ")
						feedback, _, _ = strings.Cut(feedback, ". Submit corrected parameters")
						located := hasFeedback && strings.Contains(feedback, expected) && strings.Contains(feedback, "string")
						for _, location := range locations {
							located = located && strings.Contains(feedback, location)
						}
						if !located {
							t.Error("recovery omitted the referenced parameter locations and concrete type")
						}
						if strings.Contains(prompt, "file://") || strings.Contains(prompt, "schema.json#") || strings.Contains(prompt, "private-rejected-value") {
							t.Error("recovery exposed a schema location or rejected input value")
						}
						if located && corrected {
							arguments = `{"body":{"ids":[7,64,7]}}`
						}
					}
					if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
						"message": agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{
							"name": function["name"], "arguments": arguments}}}}, "finish_reason": "tool_calls"}}}); err != nil {
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
					"records.reorder": {Name: "records.reorder", Effect: "destructive", ApprovalRequired: true, InputSchema: input},
				}}
				runtime := &reactcore.AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"records.reorder": true}, MaxModelRounds: 4}
				state, err := runtime.NewState("Reorder authorized records with exact approval", "")
				if err != nil {
					t.Fatal(err)
				}
				if err := runtime.Step(t.Context(), state, nil); err != nil {
					t.Fatal(err)
				}
				if requests.Load() != 2 || state.RoundsUsed != 2 || len(state.ModelCalls) != 2 || !state.ModelCalls[1].FormatRecovery || provider.called != 0 {
					t.Fatal("referenced-schema recovery changed its retry budget or executed before approval")
				}
				if corrected {
					if len(state.Pending) != 1 {
						t.Fatal("corrected referenced arguments did not reach normal approval")
					}
					actual, err := agentcontract.CanonicalJSON(state.Pending[0].Call.Arguments)
					if err != nil || string(actual) != `{"body":{"ids":[7,64,7]}}` {
						t.Fatal("corrected argument types, order or duplicates changed")
					}
				} else if state.Status != "failed" || len(state.Pending) != 0 {
					t.Fatal("unchanged referenced input strings were accepted or coerced")
				}
			})
		}
	}
}
