package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestRESTModelOutputProjectionKeepsArtifactPrivate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, callErr := w.Write([]byte(`{"id":"R1","status":"ready","secret":"private-token"}`)); callErr != nil {
			t.Error(callErr)
		}
	}))
	defer server.Close()
	endpoint := agentcontract.JSON{
		"name": "record.read", "description": "Read record", "method": "GET", "path": "/record", "effect": "read",
		"input_schema":  agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
		"output_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}, "status": agentcontract.JSON{"type": "string"}, "secret": agentcontract.JSON{"type": "string"}}, "required": []any{"id", "status", "secret"}},
		"model_output":  agentcontract.JSON{"paths": []any{[]any{"id"}, []any{"status"}}},
	}
	manifest := agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Read records", "base_url_env": "API_URL", "capabilities": []any{endpoint}}
	pack, err := capabilitypack.LoadRestPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(pack.Close)
	outcome, err := reactcore.ExecuteCall(context.Background(), pack, map[string]bool{"record.read": true}, agentcontract.ToolCall{CallRef: "read-1", Capability: "record.read", Arguments: agentcontract.JSON{}, Reason: "read"}, nil)
	if err != nil || outcome.Fact == nil {
		t.Fatalf("call: %+v %v", outcome, err)
	}
	fact := *outcome.Fact
	if fact.Value["data"].(map[string]any)["secret"] != "private-token" {
		t.Fatalf("full artifact lost secret: %v", fact.Value)
	}
	store := testStore(t)
	host := NewAgentHost(store, func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return pack, nil }, nil, func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{"record.read": true}, AllowModelData: true}, nil
	})
	run, err := host.Create(context.Background(), "alice", "records", "Read record", "")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(run.RunID, "alice", host.Settings.LeaseSeconds)
	if err != nil {
		t.Fatal(err)
	}
	stateForArtifact, err := host.Restore(claimed)
	if err != nil {
		t.Fatal(err)
	}
	stateForArtifact.Facts = append(stateForArtifact.Facts, fact)
	if _, err := host.save(claimed, stateForArtifact, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(run.RunID, "alice", claimed.LeaseToken); err != nil {
		t.Fatal(err)
	}
	artifact, err := store.GetArtifact(run.RunID, fact.FactID, "alice")
	if err != nil || artifact["value"].(map[string]any)["data"].(map[string]any)["secret"] != "private-token" {
		t.Fatalf("persisted artifact lost secret: %v %v", artifact, err)
	}
	previewRuntime := &reactcore.AgentRuntime{Provider: pack, Grants: map[string]bool{"record.read": true}}
	previewState, err := previewRuntime.NewState("Preview", "")
	if err != nil {
		t.Fatal(err)
	}
	previewState.Facts = []agentcontract.Fact{fact}
	view := previewRuntime.Context(previewState).Facts[0]
	if value := view.Value["data"].(map[string]any); value["id"] != "R1" || value["status"] != "ready" || value["secret"] != nil {
		t.Fatalf("preview projection: %v", view.Value)
	}
	facts := map[string]agentcontract.Fact{fact.FactID: fact}
	ancestor, err := reactcore.ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"data"}}}, facts, "", true)
	if err != nil || ancestor.(map[string]any)["secret"] != nil {
		t.Fatalf("ancestor reference exposed secret: %v %v", ancestor, err)
	}
	root, err := reactcore.ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{}}}, facts, "", true)
	if err != nil || root.(map[string]any)["data"].(map[string]any)["secret"] != nil {
		t.Fatalf("root reference exposed secret: %v %v", root, err)
	}
	_, err = reactcore.ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"data", "secret"}}}, facts, "", true)
	if err == nil || err.Error() != "fact_reference_path_invalid" {
		t.Fatalf("hidden field reference accepted: %v", err)
	}
	runtime := &reactcore.AgentRuntime{Provider: pack, Model: &coreTestModel{decisions: []agentcontract.Decision{
		{Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "secret"}},
	}}, Grants: map[string]bool{"record.read": true}, MaxModelRounds: 4}
	state, err := runtime.NewState("Read record", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []agentcontract.Fact{fact}
	packet, err := agentcontract.CanonicalJSON(runtime.Context(state))
	if err != nil || strings.Contains(string(packet), "private-token") {
		t.Fatalf("model context exposed secret: %s %v", packet, err)
	}
	if err := runtime.Step(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	inspected, callErr2 := json.Marshal(state.InspectedFact)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if strings.Contains(string(inspected), "private-token") || !strings.Contains(string(inspected), "R1") {
		t.Fatalf("ancestor inspection exposed secret: %s", inspected)
	}
	if err := runtime.Step(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.InspectedFact["error_code"] != "fact_reference_path_invalid" {
		t.Fatalf("hidden inspection accepted: %v", state.InspectedFact)
	}
}
