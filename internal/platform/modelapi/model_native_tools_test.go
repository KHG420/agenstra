package modelapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func nativeTestPacket() agentcontract.ContextPacket {
	return agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Read inventory", RuntimeFeatures: []string{"capability_search"},
		Capabilities: []agentcontract.JSON{
			{"name": "inventory.get", "description": "Read an inventory record", "input_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "integer", "minimum": 1}}, "required": []string{"id"}, "additionalProperties": false}},
			{"name": "inventory.deferred", "description": "Deferred contract", "schema_requires_inspection": true},
		}}
}

func nativeTestFunction(t *testing.T, payload agentcontract.JSON, capability string) agentcontract.JSON {
	t.Helper()
	for _, tool := range payload["tools"].([]any) {
		function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		if strings.HasPrefix(function["description"].(string), "Business capability "+capability+". ") {
			return function
		}
	}
	t.Fatalf("business function for %q missing", capability)
	return nil
}

func TestNativeToolsPreserveParameterContractsAndBudget(t *testing.T) {
	packet := nativeTestPacket()
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "native_tools"}
	payload, err := model.requestPayload(raw, "Host usage guidance must remain present.")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["response_format"]; exists || payload["tool_choice"] != "required" || payload["parallel_tool_calls"] != false {
		t.Fatal("native tool channel was not declared")
	}
	function := nativeTestFunction(t, payload, "inventory.get")
	pattern := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	names := map[string]bool{}
	for _, tool := range payload["tools"].([]any) {
		fn := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		name := fn["name"].(string)
		if !pattern.MatchString(name) || names[name] || strings.Contains(fn["description"].(string), "Business capability inventory.deferred.") {
			t.Fatal("invalid, colliding, or deferred native function", name)
		}
		names[name] = true
	}
	if names["read_skill"] || !names["search_capabilities"] || !names["tool_batch"] {
		t.Fatal("control function availability changed", names)
	}
	schema, err := agentcontract.ValidateLocalSchema(function["parameters"].(agentcontract.JSON), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, valid := range []agentcontract.JSON{{"id": 7}} {
		if err := agentcontract.ValidateSchema(schema, valid); err != nil {
			t.Fatal("literal business input rejected", err)
		}
	}
	for _, invalid := range []agentcontract.JSON{{"id": "7"}, {"id": 7, "unexpected": true}, {"id": agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": "not-a-fact", "path": []any{"id"}}}}, {"id": agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "path": []any{"data", "id"}}}}} {
		if agentcontract.ValidateSchema(schema, invalid) == nil {
			t.Fatal("invalid native argument admitted", invalid)
		}
	}
	measured, err := model.MeasureInput(packet, "Host usage guidance must remain present.")
	encoded, encodeErr := agentcontract.CanonicalJSON(payload)
	if err != nil || encodeErr != nil || measured.Tokens != int64(len(encoded)+128) {
		t.Fatal("native definitions escaped the actual request budget", measured, err, encodeErr)
	}
	after, err := agentcontract.CanonicalJSON(packet)
	if err != nil || string(after) != string(raw) {
		t.Fatal("native schema generation mutated caller context")
	}
}

func TestNativeToolsValidateDeclaredCallsWithoutRepair(t *testing.T) {
	for _, tc := range []struct {
		name, function, arguments, code string
		multiple                        bool
	}{
		{name: "business call", function: "business", arguments: `{"id":7}`},
		{name: "symbolic business input remains a proposal", function: "business", arguments: `{"id":{"$fact_value":{"fact_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","path":["data","id"]}}}`},
		{name: "evidence reference proposal", function: "tool_batch", arguments: `{"calls":[{"capability":"inventory.get","arguments":{"id":{"$fact_value":{"fact_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","path":["data","id"]}}},"reason":"Use the recorded evidence"}]}`},
		{name: "final", function: "final", arguments: `{"answer_markdown":"ok","fact_ids":[]}`},
		{name: "unknown name", function: "unknown_function", arguments: `{}`, code: "model_decision_invalid"},
		{name: "business ID is not a native alias", function: "inventory.get", arguments: `{"id":7}`, code: "model_decision_invalid"},
		{name: "no numeric coercion", function: "business", arguments: `{"id":"7"}`, code: "model_decision_invalid"},
		{name: "no argument stripping", function: "business", arguments: `{"id":7,"query":{}}`, code: "model_decision_invalid"},
		{name: "duplicate key", function: "business", arguments: `{"id":7,"id":8}`, code: "model_decision_invalid"},
		{name: "no JSON repair", function: "business", arguments: `{"id":7`, code: "model_decision_invalid"},
		{name: "wrong control wrapper", function: "final", arguments: `{"kind":"final","answer_markdown":"ok","fact_ids":[]}`, code: "model_decision_invalid"},
		{name: "mixed output calls", function: "business", arguments: `{"id":7}`, multiple: true, code: "model_decision_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packet := nativeTestPacket()
			model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "native_tools"}
			raw, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := model.requestPayload(raw, "Use declared functions.")
			if err != nil {
				t.Fatal(err)
			}
			function := tc.function
			if function == "business" {
				function = nativeTestFunction(t, payload, "inventory.get")["name"].(string)
			}
			calls := []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": function, "arguments": tc.arguments}}}
			if tc.multiple {
				calls = append(calls, agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "final", "arguments": `{"answer_markdown":"ok","fact_ids":[]}`}})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"tool_calls": calls}, "finish_reason": "tool_calls"}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model.BaseURL, model.Client, model.Timeout = server.URL, server.Client(), time.Second*5
			decision, err := model.Decide(context.Background(), packet, "Use declared functions.")
			if tc.code != "" {
				if agentcontract.ModelErrorCode(err) != tc.code {
					t.Fatalf("wrong strict rejection: got %v want %s", err, tc.code)
				}
				return
			}
			if err != nil || decision.Schema != "agenstra.decision.v1" {
				t.Fatal("native call did not produce a canonical decision", decision, err)
			}
			if tc.function == "business" && (decision.Kind != "tool_batch" || len(decision.Calls) != 1 || decision.Calls[0].Capability != "inventory.get" || !agentcontract.CallRefPattern.MatchString(decision.Calls[0].CallRef)) {
				t.Fatal("business binding or local call identity changed", decision)
			}
		})
	}
}

func TestNativeToolsReviewRemovesExecutableFunctions(t *testing.T) {
	packet := nativeTestPacket()
	packet.CompletionReview = &agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "ok", FactIDs: []string{}}
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "native_tools"}
	payload, err := model.requestPayload(raw, "Review the frozen result.")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range payload["tools"].([]any) {
		function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		names[function["name"].(string)] = true
	}
	if len(names) != 2 || !names["final"] || !names["approve_completion"] {
		t.Fatal("completion review exposed executable functions", names)
	}
	content := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
	if strings.Contains(content, "use the declared tool_batch") || !strings.Contains(content, "Only final and approve_completion are available") {
		t.Fatal("review prompt contradicts its declared functions")
	}
}

func TestNativeToolsPreserveBusinessFieldsNamedLikeDecisionFields(t *testing.T) {
	properties := agentcontract.JSON{}
	args := agentcontract.JSON{}
	for _, field := range []string{"kind", "schema", "calls", "reason", "call_ref"} {
		properties[field], args[field] = agentcontract.JSON{"type": "string"}, "business-"+field
	}
	packet := nativeTestPacket()
	packet.Capabilities[0]["input_schema"] = agentcontract.JSON{"type": "object", "properties": properties, "additionalProperties": false}
	model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "native_tools"}
	input, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := model.requestPayload(input, "Preserve business parameters.")
	if err != nil {
		t.Fatal(err)
	}
	function := nativeTestFunction(t, payload, "inventory.get")
	rawArgs, err := agentcontract.CanonicalJSON(args)
	if err != nil {
		t.Fatal(err)
	}
	call := decisionOutputToolCall{Type: "function"}
	call.Function.Name, call.Function.Arguments = function["name"].(string), string(rawArgs)
	body, detail := nativeDecisionOutputToolBody([]decisionOutputToolCall{call}, payload["tools"].([]any), packet)
	if detail != "" {
		t.Fatal("declared business fields rejected", detail)
	}
	decision, err := agentcontract.StrictDecision(body)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := agentcontract.CanonicalJSON(decision.Calls[0].Arguments)
	if err != nil || string(actual) != string(rawArgs) {
		t.Fatal("business fields stripped or overwritten", string(actual), err)
	}
}

func TestNativeToolsContextBudgetRejectsBeforeTransport(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("test", server.URL, "fixture", time.Second*5, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.DecisionOutputMode = "native_tools"
	packet := nativeTestPacket()
	packet.MaxModelInputTokens = 1
	_, err = model.Decide(t.Context(), packet, "Read inventory")
	if agentcontract.ModelErrorCode(err) != "context_too_large" || requests != 0 {
		t.Fatal("oversized native definitions reached the transport", requests, err)
	}
}
