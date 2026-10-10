package agenstra_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

type recordProvider struct{ calls int }

func (*recordProvider) Capabilities() map[string]agenstra.CapabilityDescription {
	return map[string]agenstra.CapabilityDescription{
		"records.get": {
			Name: "records.get", Version: "1", Description: "Read a record",
			Effect: "read", Replay: "safe", ReferenceScope: "durable",
			InputSchema: agenstra.JSON{"type": "object"},
		},
	}
}
func (*recordProvider) Skills() map[string]agenstra.Skill { return map[string]agenstra.Skill{} }
func (*recordProvider) SystemPrompt() string              { return "Read the requested record." }
func (*recordProvider) Close() error                      { return nil }
func (p *recordProvider) Invoke(_ context.Context, _ string, args map[string]any, _ *agenstra.InvocationContext) (agenstra.CapabilityResult, error) {
	p.calls++
	return agenstra.CapabilityResult{Data: agenstra.JSON{"id": args["id"]}, ReferenceScope: "durable"}, nil
}

type recordModel struct{}

func (recordModel) Decide(_ context.Context, packet agenstra.ContextPacket, _ string) (agenstra.Decision, error) {
	if len(packet.Facts) == 0 {
		return agenstra.Decision{Kind: "tool_batch", Calls: []agenstra.ToolCall{{
			CallRef: "read-1", Capability: "records.get",
			Arguments: agenstra.JSON{"id": "R-1"}, Reason: "Read the requested record",
		}}}, nil
	}
	return agenstra.Decision{Kind: "final", AnswerMarkdown: "Record R-1 was found.", FactIDs: []string{packet.Facts[0].FactID}}, nil
}

func TestSDKEmbeddedHostPersistsEvidenceAndOwnerIsolation(t *testing.T) {
	store, err := agenstra.NewSQLiteStore(filepath.Join(t.TempDir(), "runs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	provider := &recordProvider{}
	host := agenstra.NewAgentHost(store,
		func(context.Context, string, string) (agenstra.CapabilityProvider, error) { return provider, nil },
		recordModel{},
		func(context.Context, string, string) (agenstra.ExecutionPolicy, error) {
			return agenstra.ExecutionPolicy{GrantedCapabilities: map[string]bool{"records.get": true}, AllowModelData: true}, nil
		})
	run, err := host.Create(t.Context(), "alice", "records", "Read record R-1", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err = host.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || provider.calls != 1 {
		t.Fatalf("status=%s calls=%d error=%v", run.Status, provider.calls, err)
	}
	result, err := agenstra.EvaluateRun(t.Context(), run, agenstra.EvaluationCase{
		Name: "embedded-sdk", RunID: run.RunID, RequiredCapabilities: []string{"records.get"},
	})
	if err != nil || !result.Passed {
		t.Fatalf("evidence evaluation=%+v error=%v", result, err)
	}
	// Durable snapshots retain artifact IDs; read the full evidence from its
	// persisted artifact rather than assuming it is inlined in the run state.
	raw, err := agenstra.CanonicalJSON(run.State)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		ArtifactIDs []string `json:"artifact_ids"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ArtifactIDs) != 1 {
		t.Fatalf("artifact IDs=%v", snapshot.ArtifactIDs)
	}
	artifact, err := store.GetArtifact(run.RunID, snapshot.ArtifactIDs[0], "alice")
	if err != nil {
		t.Fatal(err)
	}
	raw, err = agenstra.CanonicalJSON(artifact)
	if err != nil {
		t.Fatal(err)
	}
	var fact agenstra.Fact
	if err := json.Unmarshal(raw, &fact); err != nil {
		t.Fatal(err)
	}
	check, err := agenstra.RequireFactValues(agenstra.FactRequirement{Capability: "records.get", Path: []any{"data", "id"}, Value: "R-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := check(t.Context(), agenstra.CompletionContext{Facts: []agenstra.Fact{fact}, FactIDs: snapshot.ArtifactIDs}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRun(run.RunID, "bob"); !errors.Is(err, agenstra.ErrRunNotFound) {
		t.Fatalf("another owner could read the run: %v", err)
	}
}

type strictRecordProvider struct{ recordProvider }

func (*strictRecordProvider) Capabilities() map[string]agenstra.CapabilityDescription {
	return map[string]agenstra.CapabilityDescription{"records.get": {
		Name: "records.get", Effect: "read", InputSchema: agenstra.JSON{
			"type": "object", "properties": agenstra.JSON{"id": agenstra.JSON{"type": "integer"}},
			"required": []string{"id"}, "additionalProperties": false,
		},
	}}
}

func TestSDKExecuteCallChecksDeclaredInputBeforeProvider(t *testing.T) {
	provider := &strictRecordProvider{}
	grants := map[string]bool{"records.get": true}
	outcome, err := agenstra.ExecuteCall(t.Context(), provider, grants, agenstra.ToolCall{Capability: "records.get", Arguments: agenstra.JSON{"id": "invalid"}}, nil)
	if err != nil || outcome.ErrorCode != "capability_input_invalid" || outcome.Fact != nil || provider.calls != 0 {
		t.Fatalf("public SDK dispatched invalid declared input: %+v, error=%v, calls=%d", outcome, err, provider.calls)
	}
	outcome, err = agenstra.ExecuteCall(t.Context(), provider, grants, agenstra.ToolCall{Capability: "records.get", Arguments: agenstra.JSON{"id": 0}}, nil)
	if err != nil || outcome.Fact == nil || outcome.ErrorCode != "" || provider.calls != 1 {
		t.Fatalf("public SDK rejected valid zero: %+v, error=%v, calls=%d", outcome, err, provider.calls)
	}
}
