package agenstra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRESTModelOutputProjectionKeepsArtifactPrivate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"R1","status":"ready","secret":"private-token"}`))
	}))
	defer server.Close()
	endpoint := JSON{
		"name": "record.read", "description": "Read record", "method": "GET", "path": "/record", "effect": "read",
		"input_schema":  JSON{"type": "object", "properties": JSON{}, "additionalProperties": false},
		"output_schema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}, "status": JSON{"type": "string"}, "secret": JSON{"type": "string"}}, "required": []any{"id", "status", "secret"}},
		"model_output":  JSON{"paths": []any{[]any{"id"}, []any{"status"}}},
	}
	manifest := JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Read records", "base_url_env": "API_URL", "capabilities": []any{endpoint}}
	pack, err := LoadRestPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	outcome, err := ExecuteCall(context.Background(), pack, map[string]bool{"record.read": true}, ToolCall{CallRef: "read-1", Capability: "record.read", Arguments: JSON{}, Reason: "read"}, nil)
	if err != nil || outcome.Fact == nil {
		t.Fatalf("call: %+v %v", outcome, err)
	}
	fact := *outcome.Fact
	if fact.Value["data"].(map[string]any)["secret"] != "private-token" {
		t.Fatalf("full artifact lost secret: %v", fact.Value)
	}
	store := testStore(t)
	host := NewAgentHost(store, func(context.Context, string, string) (CapabilityProvider, error) { return pack, nil }, nil, func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{GrantedCapabilities: map[string]bool{"record.read": true}, AllowModelData: true}, nil
	})
	run, err := host.Create(context.Background(), "alice", "records", "Read record", "")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(run.RunID, "alice", host.Settings.LeaseSeconds)
	if err != nil {
		t.Fatal(err)
	}
	stateForArtifact, err := host.restore(claimed)
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
	view := factView(fact, 6000)
	if value := view.Value["data"].(map[string]any); value["id"] != "R1" || value["status"] != "ready" || value["secret"] != nil {
		t.Fatalf("preview projection: %v", view.Value)
	}
	facts := map[string]Fact{fact.FactID: fact}
	ancestor, err := ResolveArgument(JSON{"$fact_value": JSON{"fact_id": fact.FactID, "path": []any{"data"}}}, facts, "", true)
	if err != nil || ancestor.(map[string]any)["secret"] != nil {
		t.Fatalf("ancestor reference exposed secret: %v %v", ancestor, err)
	}
	root, err := ResolveArgument(JSON{"$fact_value": JSON{"fact_id": fact.FactID, "path": []any{}}}, facts, "", true)
	if err != nil || root.(map[string]any)["data"].(map[string]any)["secret"] != nil {
		t.Fatalf("root reference exposed secret: %v %v", root, err)
	}
	_, err = ResolveArgument(JSON{"$fact_value": JSON{"fact_id": fact.FactID, "path": []any{"data", "secret"}}}, facts, "", true)
	if err == nil || err.Error() != "fact_reference_path_invalid" {
		t.Fatalf("hidden field reference accepted: %v", err)
	}
	runtime := &AgentRuntime{Provider: pack, Model: &coreTestModel{decisions: []Decision{
		{Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "secret"}},
	}}, Grants: map[string]bool{"record.read": true}, MaxModelRounds: 4}
	state, err := runtime.NewState("Read record", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []Fact{fact}
	packet, err := CanonicalJSON(runtime.Context(state))
	if err != nil || strings.Contains(string(packet), "private-token") {
		t.Fatalf("model context exposed secret: %s %v", packet, err)
	}
	if err := runtime.Step(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	inspected, _ := json.Marshal(state.InspectedFact)
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

func TestModelOutputEmptyAndLegacyBehavior(t *testing.T) {
	full := JSON{"data": JSON{"id": "R1", "secret": "private-token"}}
	legacy := Fact{FactID: NewID(), Value: full}
	if got := factView(legacy, 6000).Value["data"].(map[string]any)["secret"]; got != "private-token" {
		t.Fatalf("nil model_output changed legacy preview: %v", got)
	}
	opaque := Fact{FactID: NewID(), Value: full, ModelOutput: &ModelOutput{Paths: [][]string{}}}
	if got := factView(opaque, 6000).Value["data"].(map[string]any); len(got) != 0 {
		t.Fatalf("empty paths exposed result: %v", got)
	}
	_, err := ResolveArgument(JSON{"$fact_value": JSON{"fact_id": opaque.FactID, "path": []any{"data", "id"}}}, map[string]Fact{opaque.FactID: opaque}, "", true)
	if err == nil || err.Error() != "fact_reference_path_invalid" {
		t.Fatalf("opaque result reference accepted: %v", err)
	}
}

func TestModelOutputRejectsInvalidPaths(t *testing.T) {
	for _, output := range []*ModelOutput{
		{Paths: nil},
		{Paths: [][]string{{}}},
		{Paths: [][]string{{"items", "0", "secret"}, {"items", "0", "secret"}}},
		{Paths: [][]string{{""}}},
		{Paths: [][]string{{strings.Repeat("x", 129)}}},
	} {
		if err := validateModelOutput(output); err == nil {
			t.Fatalf("accepted invalid projection: %+v", output)
		}
	}
	if err := validateModelOutput(&ModelOutput{Paths: [][]string{}}); err != nil {
		t.Fatalf("explicit empty projection rejected: %v", err)
	}
	schema := JSON{"type": "object", "properties": JSON{"items": JSON{"type": "array", "items": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}}}}}
	if err := validateModelOutputSchema(&ModelOutput{Paths: [][]string{{"items", "0", "id"}}}, schema); err == nil {
		t.Fatal("accepted a partial array index projection")
	}
	manifest := JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Read", "base_url_env": "API_URL", "capabilities": []any{JSON{
		"name": "records.read", "description": "Read", "method": "GET", "path": "/records", "effect": "read",
		"input_schema": JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}, "output_schema": schema,
		"model_output": JSON{"paths": []any{[]any{"items", "0", "id"}}},
	}}}
	if err := ValidatePackManifest(manifest, nil); err == nil {
		t.Fatal("manifest accepted partial array projection")
	}
	visible := projectedModelData(JSON{"items": []any{JSON{"id": "A", "secret": "hidden"}}}, &ModelOutput{Paths: [][]string{{"items", "0", "id"}}})
	if len(visible) != 0 {
		t.Fatalf("array index was interpreted as object field: %v", visible)
	}
}

func TestCustomProviderRejectsInvalidModelOutputBeforeInvoke(t *testing.T) {
	for _, output := range []*ModelOutput{{Paths: nil}, {Paths: [][]string{{}}}, {Paths: [][]string{{""}}}} {
		cap := CapabilityDescription{Name: "record.read", ModelOutput: output}
		provider := &coreTestProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}
		outcome, err := ExecuteCall(context.Background(), provider, map[string]bool{cap.Name: true}, ToolCall{Capability: cap.Name, Arguments: JSON{}}, nil)
		if err != nil || outcome.Fact != nil || outcome.ErrorCode != "model_output_config_invalid" || provider.called != 0 {
			t.Fatalf("invalid custom projection reached provider or fact: %+v %v calls=%d", outcome, err, provider.called)
		}
		fact := Fact{Value: JSON{"data": JSON{"secret": "private"}}, ModelOutput: output}
		if visible := modelFactValue(fact)["data"].(map[string]any); len(visible) != 0 {
			t.Fatalf("invalid persisted projection exposed data: %v", visible)
		}
	}
	for _, output := range []*ModelOutput{nil, {Paths: [][]string{}}} {
		cap := CapabilityDescription{Name: "record.read", ModelOutput: output}
		provider := &coreTestProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}
		outcome, err := ExecuteCall(context.Background(), provider, map[string]bool{cap.Name: true}, ToolCall{Capability: cap.Name, Arguments: JSON{}}, nil)
		if err != nil || outcome.Fact == nil || provider.called != 1 {
			t.Fatalf("valid custom projection rejected: %+v %v calls=%d", outcome, err, provider.called)
		}
	}
}
