package agenstra_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

type utf8ReceiptDecisionModel struct{ calls int }

func (m *utf8ReceiptDecisionModel) Decide(context.Context, agenstra.ContextPacket, string) (agenstra.Decision, error) {
	m.calls++
	if m.calls != 1 {
		return agenstra.Decision{}, errors.New("an uncertain write must not return to the model")
	}
	return agenstra.Decision{Kind: "tool_batch", Calls: []agenstra.ToolCall{{CallRef: "write-once", Capability: "records.write", Arguments: agenstra.JSON{}, Reason: "Write the original record once"}}}, nil
}

func TestSDKInvalidUTF8WriteReceiptRemainsUnknownAcrossRestore(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte("{\"code\":0,\"data\":{\"id\":\"R\xff1\"}}")); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	manifest := agenstra.JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Use actual record receipts", "base_url_env": "API",
		"capabilities": []any{agenstra.JSON{"name": "records.write", "description": "Write the record once", "method": "POST", "path": "/record", "effect": "write",
			"input_schema":  agenstra.JSON{"type": "object", "properties": agenstra.JSON{}, "additionalProperties": false},
			"output_schema": agenstra.JSON{"type": "object", "properties": agenstra.JSON{"id": agenstra.JSON{"type": "string"}}, "required": []string{"id"}},
			"response_path": []string{"data"}, "business_success": agenstra.JSON{"path": []string{"code"}, "value": 0, "error_code": "business_rejected"}}}}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pack.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := agenstra.NewSQLiteStore(filepath.Join(t.TempDir(), "runs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	defer client.CloseIdleConnections()
	model := &utf8ReceiptDecisionModel{}
	host := agenstra.NewAgentHost(store, func(context.Context, string, string) (agenstra.CapabilityProvider, error) {
		return agenstra.LoadRestPack(path, map[string]string{"API": server.URL}, client)
	}, model, func(context.Context, string, string) (agenstra.ExecutionPolicy, error) {
		return agenstra.ExecutionPolicy{Subject: "owner", AllowModelData: true, GrantedCapabilities: map[string]bool{"records.write": true}}, nil
	})
	run, err := host.Create(t.Context(), "owner", "records", "Write the record once", "utf8-receipt")
	if err != nil {
		t.Fatal(err)
	}
	var invocationID, argumentsHash string
	for range 3 {
		run, err = host.Drive(t.Context(), run.RunID, "owner")
		if err != nil {
			t.Fatal(err)
		}
		state, err := host.Restore(run)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "needs_reconciliation" || len(state.Facts) != 0 || len(state.Pending) != 1 || model.calls != 1 || requests.Load() != 1 {
			t.Fatalf("invalid receipt was settled or replayed: status=%s facts=%d pending=%d model=%d HTTP=%d", run.Status, len(state.Facts), len(state.Pending), model.calls, requests.Load())
		}
		item := state.Pending[0]
		if item.Status != "unknown" || item.Attempts != 1 || item.ErrorCode == nil || *item.ErrorCode != "upstream_response_invalid" || item.Receipt == nil || item.Receipt.Status != "unknown" || item.Receipt.FactID != "" {
			t.Fatalf("original unknown receipt was not retained: %+v", item)
		}
		if invocationID == "" {
			invocationID, argumentsHash = item.InvocationID, item.ArgumentsSHA256
		}
		if item.InvocationID != invocationID || item.ArgumentsSHA256 != argumentsHash || len(argumentsHash) != 64 {
			t.Fatal("restored invocation identity or arguments changed")
		}
	}
}
