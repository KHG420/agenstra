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

func TestDecisionSchemaRecoveryDiagnosesSelectedKind(t *testing.T) {
	for _, test := range []struct {
		mode    string
		kind    string
		missing string
		repeat  bool
	}{
		{mode: "json_schema", kind: "tool_batch", missing: "calls"},
		{mode: "json_schema", kind: "inspect_fact", missing: "fact_id"},
		{mode: "json_schema", kind: "search_capabilities", missing: "query"},
		{mode: "output_tools", kind: "tool_batch", missing: "calls"},
		{mode: "json_schema", kind: "tool_batch", missing: "calls", repeat: true},
	} {
		name := test.mode + "/" + test.kind
		if test.repeat {
			name += "/unchanged_invalid"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var payload agentcontract.JSON
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				body := `{"kind":"` + test.kind + `"}`
				if requests.Add(1) == 2 {
					prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
					_, feedback, found := strings.Cut(prompt, "The rejected output failed its declared parameter schema: ")
					if !found || !strings.Contains(feedback, test.missing) || strings.Contains(feedback, "value must be") || strings.Contains(feedback, "const") {
						t.Errorf("recovery did not identify the selected %s decision's missing %s: %s", test.kind, test.missing, feedback)
					}
					if !test.repeat {
						body = `{"kind":"final","answer_markdown":"No business call was executed.","fact_ids":[]}`
					}
				}
				message := agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{
					"name": "submit_decision", "arguments": body}}}}
				finish := "tool_calls"
				if test.mode == "json_schema" {
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
			model.DecisionOutputMode = test.mode
			provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
			runtime := &reactcore.AgentRuntime{Model: model, Provider: provider, MaxModelRounds: 3, MaxContextCapabilities: 4}
			state, err := runtime.NewState("Do not execute any business capability. Finish with an explanation.", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			status := "completed"
			if test.repeat {
				status = "failed"
			}
			if state.Status != status || requests.Load() != 2 || len(state.ModelCalls) != 2 || !state.ModelCalls[1].FormatRecovery || provider.called != 0 {
				t.Fatal("diagnostic recovery changed rejection, budget, or execution", state)
			}
		})
	}
}

func TestDecisionDiagnosticRetainsRootConstraintsAndOriginalSchema(t *testing.T) {
	schema := nativeDecisionOutputSchema(decisionOutputTools(false))
	before, err := agentcontract.CanonicalJSON(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing root identity", body: `{"kind":"tool_batch"}`, want: "schema"},
		{name: "wrong root identity", body: `{"schema":"different","kind":"tool_batch"}`, want: "agenstra.decision.v1"},
		{name: "unknown root property", body: `{"schema":"agenstra.decision.v1","kind":"tool_batch","unexpected":true}`, want: "unexpected"},
		{name: "unknown kind", body: `{"schema":"agenstra.decision.v1","kind":"unknown"}`, want: "kind"},
		{name: "nonobject decision", body: `[]`, want: "object"},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic := decisionDiagnosticSchema([]byte(test.body), schema)
			compiled, err := agentcontract.ValidateLocalSchema(diagnostic, false)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal([]byte(test.body), &value); err != nil {
				t.Fatal(err)
			}
			err = agentcontract.ValidateSchema(compiled, value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatal("selected diagnostic lost a root constraint", test.want, err)
			}
			after, err := agentcontract.CanonicalJSON(schema)
			if err != nil || string(after) != string(before) {
				t.Fatal("diagnostic mutated the original acceptance schema", err)
			}
		})
	}
}
