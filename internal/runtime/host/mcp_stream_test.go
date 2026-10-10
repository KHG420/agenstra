package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
)

func TestMCPInvalidWriteReceiptPausesWithoutReplaying(t *testing.T) {
	for _, responseCase := range []string{"incomplete-sse", "missing-version", "wrong-version", "result-and-null-error", "request-with-result", "string-error-flag", "null-error-flag", "object-error-flag"} {
		t.Run(responseCase, func(t *testing.T) { testMCPInvalidWriteReceipt(t, responseCase) })
	}
}

func testMCPInvalidWriteReceipt(t *testing.T, responseCase string) {
	t.Helper()
	tool := agentcontract.JSON{"name": "record.create", "description": "Create record", "inputSchema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false}, "outputSchema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request agentcontract.JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var result agentcontract.JSON
		switch request["method"] {
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "initialize":
			result = agentcontract.JSON{"protocolVersion": "2025-03-26", "capabilities": agentcontract.JSON{}, "serverInfo": agentcontract.JSON{"name": "test", "version": "1"}}
		case "tools/list":
			result = agentcontract.JSON{"tools": []any{tool}}
		case "tools/call":
			writes.Add(1)
			result = agentcontract.JSON{"content": []any{}, "structuredContent": agentcontract.JSON{"id": "R1"}}
			reply := agentcontract.JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}
			switch responseCase {
			case "incomplete-sse":
				w.Header().Set("Content-Type", "text/event-stream")
				raw, callErr8 := json.Marshal(reply)
				if callErr8 != nil {
					t.Error(callErr8)
				}
				if _, callErr9 := fmt.Fprintf(w, "data: %s\ndata: incomplete\n\n", raw); callErr9 != nil {
					t.Error(callErr9)
				}
				return
			case "missing-version":
				delete(reply, "jsonrpc")
			case "wrong-version":
				reply["jsonrpc"] = "1.0"
			case "result-and-null-error":
				reply["error"] = nil
			case "request-with-result":
				reply["method"] = "ping"
			case "string-error-flag":
				result["isError"] = "true"
			case "null-error-flag":
				result["isError"] = nil
			case "object-error-flag":
				result["isError"] = agentcontract.JSON{}
			}
			w.Header().Set("Content-Type", "application/json")
			if callErr10 := json.NewEncoder(w).Encode(reply); callErr10 != nil {
				t.Error(callErr10)
			}
			return
		default:
			t.Errorf("unexpected method: %v", request["method"])
		}
		w.Header().Set("Content-Type", "application/json")
		if callErr11 := json.NewEncoder(w).Encode(agentcontract.JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}); callErr11 != nil {
			t.Error(callErr11)
		}
	}))
	defer server.Close()
	manifest := agentcontract.JSON{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1", "guidance": "Create records", "source": agentcontract.JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{agentcontract.JSON{"name": tool["name"], "effect": "write", "contract_sha256": capabilitypack.MCPContractDigest(tool)}}}
	pack, err := capabilitypack.OpenMCPPack(t.Context(), writeTestManifest(t, manifest), map[string]string{"MCP_URL": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(pack.Close)
	model := &hostModel{decisions: []agentcontract.Decision{{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "create-1", Capability: "record.create", Arguments: agentcontract.JSON{}, Reason: "Create the requested record"}}}}}
	host := NewAgentHost(testStore(t), func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return pack, nil }, model, func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{"record.create": true}, AllowModelData: true}, nil
	})
	run, err := host.Create(t.Context(), "alice", "records", "Create one record", "invalid-mcp-write")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		run, err = host.Drive(t.Context(), run.RunID, "alice")
		if err != nil || run.Status != "needs_reconciliation" {
			t.Fatalf("invalid receipt confirmed the write: status=%s err=%v", run.Status, err)
		}
		if writes.Load() != 1 {
			t.Fatalf("business operation replayed: %d", writes.Load())
		}
		state, err := host.Restore(run)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Facts) != 0 || len(state.Pending) != 1 || state.Pending[0].Status != "unknown" || state.Pending[0].Receipt == nil || state.Pending[0].Receipt.Status != "unknown" {
			t.Fatal("invalid write receipt became confirmed execution evidence", state.Pending)
		}
	}
}
