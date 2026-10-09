package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPHTTPPaginationContractAndSessionCleanup(t *testing.T) {
	tool := JSON{"name": "Metrics.Capacity/v2", "description": "Read capacity.", "inputSchema": JSON{"type": "object", "required": []any{"resource"}, "properties": JSON{"resource": JSON{"type": "integer", "minimum": 1}}, "additionalProperties": false}, "outputSchema": JSON{"type": "object", "required": []any{"capacity"}, "properties": JSON{"capacity": JSON{"type": "integer"}}, "additionalProperties": false}}
	calls := 0
	deletes := 0
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing connection credential")
		}
		if r.Method == http.MethodDelete {
			deletes++
			if r.Header.Get("Mcp-Session-Id") != "session-1" {
				t.Errorf("missing session on DELETE")
			}
			if r.Header.Get("Mcp-Protocol-Version") != "2025-06-18" {
				t.Errorf("missing negotiated protocol on DELETE")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != "POST" {
			t.Errorf("wrong method: %s", r.Method)
		}
		var request JSON
		if callErr2 := json.NewDecoder(r.Body).Decode(&request); callErr2 != nil {
			t.Error(callErr2)
		}
		method, _ := request["method"].(string)
		version := "2025-06-18"
		if method == "initialize" {
			version = "2025-03-26"
		}
		if r.Header.Get("Mcp-Protocol-Version") != version {
			t.Errorf("incorrect protocol header for %s", method)
		}
		if method != "initialize" && r.Header.Get("Mcp-Session-Id") != "session-1" {
			t.Errorf("missing session on %s", method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "session-1")
		if method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch method {
		case "initialize":
			result = JSON{"protocolVersion": "2025-06-18", "capabilities": JSON{}, "serverInfo": JSON{"name": "test", "version": "1"}}
		case "tools/list":
			pages++
			params, _ := request["params"].(map[string]any)
			if params["cursor"] == nil {
				result = JSON{"tools": []any{}, "nextCursor": "p2"}
			} else {
				result = JSON{"tools": []any{tool}}
			}
		case "tools/call":
			calls++
			result = JSON{"content": []any{}, "structuredContent": JSON{"capacity": 2400}}
		default:
			t.Errorf("unexpected method %s", method)
		}
		if callErr3 := json.NewEncoder(w).Encode(JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}); callErr3 != nil {
			t.Error(callErr3)
		}
	}))
	defer server.Close()
	manifest := JSON{"schema": "agenstra.mcp-pack.v1", "name": "metrics", "version": "1", "guidance": "Use metrics", "source": JSON{"transport": "streamable_http", "url_env": "MCP_URL", "token_env": "MCP_TOKEN"}, "tools": []any{JSON{"name": tool["name"], "effect": "read", "contract_sha256": MCPContractDigest(tool)}}}
	path := writeTestManifest(t, manifest)
	pack, err := OpenMCPPack(context.Background(), path, map[string]string{"MCP_URL": server.URL, "MCP_TOKEN": "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Fatalf("pagination: %d", pages)
	}
	bad, err := pack.Invoke(context.Background(), "Metrics.Capacity/v2", JSON{"resource": "8"}, nil)
	if err != nil || bad.ErrorCode != "capability_input_invalid" || calls != 0 {
		t.Fatalf("invalid args escaped: %+v %v", bad, err)
	}
	good, err := pack.Invoke(context.Background(), "Metrics.Capacity/v2", JSON{"resource": 8}, nil)
	if err != nil || fmt.Sprint(good.Data["capacity"]) != "2400" || calls != 1 {
		t.Fatalf("MCP result: %+v %v", good, err)
	}
	if err := pack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pack.Close(); err != nil {
		t.Fatal(err)
	}
	if deletes != 1 {
		t.Fatalf("session cleanup count: %d", deletes)
	}
	tool["description"] = "drifted"
	_, err = OpenMCPPack(context.Background(), path, map[string]string{"MCP_URL": server.URL, "MCP_TOKEN": "test-token"})
	if err == nil || !strings.Contains(err.Error(), "contract changed") {
		t.Fatalf("contract drift accepted: %v", err)
	}
}
