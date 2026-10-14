package host

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestRESTNullableModelOutputKeepsPrivateArtifactAndReferences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"items":[{"id":1,"pricing":null,"secret":"private-array-token"},{"id":2,"pricing":[],"secret":"second-private-token"},{"id":3,"pricing":[{"amount":0,"secret":"private-price"}],"secret":"third-private-token"}]}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	endpoint := agentcontract.JSON{
		"name": "records.read", "description": "Read records", "method": "GET", "path": "/records", "effect": "read",
		"input_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
		"output_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"items": agentcontract.JSON{"type": "array", "items": agentcontract.JSON{
			"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "integer"}, "pricing": agentcontract.JSON{"anyOf": []any{agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"amount": agentcontract.JSON{"type": "number"}, "secret": agentcontract.JSON{"type": "string"}}, "required": []any{"amount", "secret"}}}, agentcontract.JSON{"type": "null"}}}, "secret": agentcontract.JSON{"type": "string"}},
			"required": []any{"id", "pricing", "secret"},
		}}}, "required": []any{"items"}},
		"model_output": agentcontract.JSON{"paths": []any{[]any{"items", "*", "id"}, []any{"items", "*", "pricing", "*", "amount"}}},
	}
	manifest := agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Read records", "base_url_env": "API_URL", "capabilities": []any{endpoint}}
	pack, err := capabilitypack.LoadRestPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := pack.Close(); err != nil {
			t.Error(err)
		}
	}()
	outcome, err := reactcore.ExecuteCall(context.Background(), pack, map[string]bool{"records.read": true}, agentcontract.ToolCall{CallRef: "read-1", Capability: "records.read", Arguments: agentcontract.JSON{}, Reason: "read"}, nil)
	if err != nil || outcome.Fact == nil {
		t.Fatalf("REST array call: %+v %v", outcome, err)
	}
	fact := *outcome.Fact
	full, err := agentcontract.CanonicalJSON(fact.Value)
	if err != nil || !strings.Contains(string(full), "private-array-token") {
		t.Fatalf("full evidence lost private field: %s %v", full, err)
	}
	facts := map[string]agentcontract.Fact{fact.FactID: fact}
	for _, path := range [][]any{{}, {"data"}, {"data", "items"}, {"data", "items", 0}} {
		value, err := reactcore.ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": path}}, facts, "", true)
		if err != nil {
			t.Fatal(err)
		}
		visible, err := agentcontract.CanonicalJSON(value)
		if err != nil || strings.Contains(string(visible), "private") {
			t.Fatalf("ancestor reference exposed hidden field: %s %v", visible, err)
		}
	}
	_, err = reactcore.ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"data", "items", 0, "secret"}}}, facts, "", true)
	if err == nil || err.Error() != "fact_reference_path_invalid" {
		t.Fatalf("hidden array reference accepted: %v", err)
	}
	runtime := &reactcore.AgentRuntime{Provider: pack, Grants: map[string]bool{"records.read": true}, MaxModelRounds: 4,
		Model: &coreTestModel{decisions: []agentcontract.Decision{
			{Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "items"}},
			{Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "items", 0, "secret"}},
		}}}
	state, err := runtime.NewState("Inspect records", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []agentcontract.Fact{fact}
	packet, err := agentcontract.CanonicalJSON(runtime.Context(state))
	if err != nil || strings.Contains(string(packet), "private-array-token") || strings.Contains(string(packet), "second-private-token") {
		t.Fatalf("array preview exposed secret: %s %v", packet, err)
	}
	if err := runtime.Step(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	inspection, err := agentcontract.CanonicalJSON(state.InspectedFact)
	if err != nil || strings.Contains(string(inspection), "private") || !strings.Contains(string(inspection), `"amount":0`) {
		t.Fatalf("array inspection lost zero or exposed secret: %s %v", inspection, err)
	}
	if err := runtime.Step(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.InspectedFact["error_code"] != "fact_reference_path_invalid" {
		t.Fatalf("hidden inspection accepted: %v", state.InspectedFact)
	}
	value, err := reactcore.ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"data", "items", 0, "pricing"}}}, facts, "", true)
	if err != nil || value != nil {
		t.Fatalf("explicit null reference changed: %v %v", value, err)
	}
	_, err = reactcore.ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"data", "items", 2, "pricing", 0, "secret"}}}, facts, "", true)
	if err == nil {
		t.Fatal("nullable nested private field reference accepted")
	}
	if !strings.Contains(string(inspection), `"pricing":null`) || !strings.Contains(string(inspection), `"pricing":[]`) {
		t.Fatalf("null and empty arrays changed in inspection: %s", inspection)
	}
	// The provider result remains complete after every model-facing access.
	after, err := agentcontract.CanonicalJSON(fact.Value)
	if err != nil || string(after) != string(full) {
		t.Fatalf("model access changed complete evidence: %s %v", after, err)
	}
}
