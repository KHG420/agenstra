package agenstra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMCPPackConnectsAndInvokesUsingMultilineSSE(t *testing.T) {
	tool := JSON{"name": "record.read", "description": "Read record", "inputSchema": JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}, "outputSchema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request["method"] == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result JSON
		switch request["method"] {
		case "initialize":
			result = JSON{"protocolVersion": "2025-03-26", "capabilities": JSON{}, "serverInfo": JSON{"name": "test", "version": "1"}}
		case "tools/list":
			result = JSON{"tools": []any{tool}}
		case "tools/call":
			result = JSON{"content": []any{}, "structuredContent": JSON{"id": "R1"}}
		default:
			t.Errorf("unexpected method: %v", request["method"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if _, callErr5 := fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n"); callErr5 != nil {
			t.Error(callErr5)
		}
		raw, err := json.MarshalIndent(JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if _, callErr6 := fmt.Fprintf(w, "data: %s\n", line); callErr6 != nil {
				t.Error(callErr6)
			}
		}
		if _, callErr7 := fmt.Fprint(w, "\n"); callErr7 != nil {
			t.Error(callErr7)
		}
	}))
	defer server.Close()
	manifest := JSON{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1", "guidance": "Read records", "source": JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{JSON{"name": tool["name"], "effect": "read", "contract_sha256": MCPContractDigest(tool)}}}
	pack, err := OpenMCPPack(t.Context(), writeTestManifest(t, manifest), map[string]string{"MCP_URL": server.URL})
	if err != nil {
		t.Fatalf("valid streaming server cannot be connected: %v", err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(pack.Close)
	result, err := pack.Invoke(t.Context(), "record.read", JSON{}, nil)
	if err != nil || result.ErrorCode != "" || result.Data["id"] != "R1" {
		t.Fatalf("streaming business result lost: %+v %v", result, err)
	}
}

func TestMCPInvalidWriteReceiptPausesWithoutReplaying(t *testing.T) {
	for _, responseCase := range []string{"incomplete-sse", "missing-version", "wrong-version", "result-and-null-error", "request-with-result"} {
		t.Run(responseCase, func(t *testing.T) { testMCPInvalidWriteReceipt(t, responseCase) })
	}
}

func testMCPInvalidWriteReceipt(t *testing.T, responseCase string) {
	t.Helper()
	tool := JSON{"name": "record.create", "description": "Create record", "inputSchema": JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}, "outputSchema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var result JSON
		switch request["method"] {
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "initialize":
			result = JSON{"protocolVersion": "2025-03-26", "capabilities": JSON{}, "serverInfo": JSON{"name": "test", "version": "1"}}
		case "tools/list":
			result = JSON{"tools": []any{tool}}
		case "tools/call":
			writes.Add(1)
			result = JSON{"content": []any{}, "structuredContent": JSON{"id": "R1"}}
			reply := JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}
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
		if callErr11 := json.NewEncoder(w).Encode(JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}); callErr11 != nil {
			t.Error(callErr11)
		}
	}))
	defer server.Close()
	manifest := JSON{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1", "guidance": "Create records", "source": JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{JSON{"name": tool["name"], "effect": "write", "contract_sha256": MCPContractDigest(tool)}}}
	pack, err := OpenMCPPack(t.Context(), writeTestManifest(t, manifest), map[string]string{"MCP_URL": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(pack.Close)
	model := &hostModel{decisions: []Decision{{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "create-1", Capability: "record.create", Arguments: JSON{}, Reason: "Create the requested record"}}}}}
	host := NewAgentHost(testStore(t), func(context.Context, string, string) (CapabilityProvider, error) { return pack, nil }, model, func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{GrantedCapabilities: map[string]bool{"record.create": true}, AllowModelData: true}, nil
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
	}
}
