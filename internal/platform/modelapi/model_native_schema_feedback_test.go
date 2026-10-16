package modelapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func numericArrayInput() agentcontract.JSON {
	return agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
		"body": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
			"ids": agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "integer"}},
		}, "required": []string{"ids"}, "additionalProperties": false},
	}, "required": []string{"body"}, "additionalProperties": false}
}

func TestNativeSchemaRecoveryIdentifiesArrayElementTypes(t *testing.T) {
	for _, corrected := range []bool{true, false} {
		t.Run(map[bool]string{true: "corrected submission", false: "unchanged invalid submission"}[corrected], func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				function := nativeTestFunction(t, payload, "records.reorder")
				arguments := `{"body":{"ids":["7",64,"7"]}}`
				if n == 2 {
					prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
					located := strings.Contains(prompt, "/body/ids/0") && strings.Contains(prompt, "/body/ids/2") && strings.Contains(prompt, "integer")
					if !located {
						t.Error("recovery omitted the rejected array element locations and integer contract")
					}
					if strings.Contains(prompt, "file://") || strings.Contains(prompt, "schema.json#") {
						t.Error("recovery exposed a local validator schema location")
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
				"records.reorder": {Name: "records.reorder", Effect: "destructive", ApprovalRequired: true, InputSchema: numericArrayInput()},
			}}
			runtime := &reactcore.AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"records.reorder": true}, MaxModelRounds: 4}
			state, err := runtime.NewState("Reorder authorized records using integer IDs, with exact approval", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 || state.RoundsUsed != 2 || len(state.ModelCalls) != 2 || !state.ModelCalls[1].FormatRecovery || provider.called != 0 {
				t.Fatal("schema recovery changed the existing retry budget or executed before approval", requests.Load(), state)
			}
			if corrected {
				if len(state.Pending) != 1 {
					t.Fatal("corrected submission was not retained for normal approval", state)
				}
				actual, err := agentcontract.CanonicalJSON(state.Pending[0].Call.Arguments)
				if err != nil || string(actual) != `{"body":{"ids":[7,64,7]}}` {
					t.Fatal("corrected numeric IDs, order or duplicates were changed", string(actual), err)
				}
			} else if state.Status != "failed" || len(state.Pending) != 0 {
				t.Fatal("unchanged invalid strings were coerced or accepted", state)
			}
		})
	}
}

func TestNativeSchemaDecisionErrorKeepsSafeCodeAndValidationCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload agentcontract.JSON
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		function := nativeTestFunction(t, payload, "records.reorder")
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
			"message": agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{
				"name": function["name"], "arguments": `{"body":{"ids":["private-rejected-value"]}}`}}}}, "finish_reason": "tool_calls"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("test", server.URL, "fixture-key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.DecisionOutputMode = "native_tools"
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Capabilities: []agentcontract.JSON{
		{"name": "records.reorder", "description": "Reorder records", "input_schema": numericArrayInput()},
	}}
	decision, err := model.Decide(t.Context(), packet, "Use the declared input contract")
	if err == nil || err.Error() != "model_decision_invalid" || agentcontract.ModelErrorCode(err) != "model_decision_invalid" || decision.Kind != "" {
		t.Fatal("invalid schema response did not preserve its safe error code", err, decision)
	}
	var coded agentcontract.ModelDecisionError
	var validation *jsonschema.ValidationError
	if !errors.As(err, &coded) || !errors.As(err, &validation) {
		t.Fatal("schema error lost its typed validation cause or existing error identity", err)
	}
}

func TestDecisionSchemaCauseAlsoSurvivesOtherSchemaOutputModes(t *testing.T) {
	for _, mode := range []string{"output_tools", "json_schema"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := `{"kind":"inspect_fact","fact_id":"saved-fact","path":[],"limit":"private-rejected-limit"}`
				message := agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{
					"name": "submit_decision", "arguments": body}}}}
				finish := "tool_calls"
				if mode == "json_schema" {
					message = agentcontract.JSON{"content": `{"schema":"agenstra.decision.v1",` + body[1:]}
					finish = "stop"
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
					"message": message, "finish_reason": finish}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "fixture-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = mode
			decision, err := model.Decide(t.Context(), nativeTestPacket(), "Inspect saved evidence")
			var validation *jsonschema.ValidationError
			var typeError *json.UnmarshalTypeError
			var coded agentcontract.ModelDecisionError
			if err == nil || err.Error() != "model_decision_invalid" || !errors.As(err, &coded) {
				t.Fatal("schema mode lost safe error identity or diagnostic cause", err)
			}
			if mode == "json_schema" && !errors.As(err, &validation) || mode == "output_tools" && !errors.As(err, &typeError) {
				t.Fatal("schema mode lost the original schema or typed-decoder cause", err)
			}
			if decision.Kind != "" || decision.ModelCall.FormatError != "model_decision_schema_invalid" {
				t.Fatal("invalid structured response was accepted or reclassified", decision)
			}
		})
	}
}
