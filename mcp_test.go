package agenstra

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMCPStdioCancellationReapsProcess(t *testing.T) {
	if os.Getenv("AGENSTRA_MCP_HANG_HELPER") == "1" {
		reader := bufio.NewReader(os.Stdin)
		if _, callErr := reader.ReadString('\n'); callErr != nil {
			t.Error(callErr)
		}
		time.Sleep(30 * time.Second)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMCPStdioCancellationReapsProcess")
	cmd.Env = append(os.Environ(), "AGENSTRA_MCP_HANG_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	client := &stdioMCP{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = client.Request(ctx, "tools/list", JSON{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wanted deadline, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("cancellation did not interrupt blocking read")
	}
	if cmd.ProcessState == nil {
		t.Fatal("MCP child not reaped")
	}
	if err := client.Close(); err != nil && !strings.Contains(err.Error(), "killed") {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestMCPHTTPPaginationContractAndSessionCleanup(t *testing.T) {
	tool := JSON{"name": "Metrics.Capacity/v2", "description": "Read capacity.", "inputSchema": JSON{"type": "object", "required": []any{"resource"}, "properties": JSON{"resource": JSON{"type": "integer", "minimum": 1}}, "additionalProperties": false}, "outputSchema": JSON{"type": "object", "required": []any{"capacity"}, "properties": JSON{"capacity": JSON{"type": "integer"}}, "additionalProperties": false}}
	calls := 0
	deletes := 0
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes++
			if r.Header.Get("Mcp-Session-Id") != "session-1" {
				t.Errorf("missing session on DELETE")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Header.Get("Mcp-Protocol-Version") != "2025-03-26" {
			t.Errorf("missing protocol header")
		}
		if r.Method != "POST" {
			t.Errorf("wrong method: %s", r.Method)
		}
		var request JSON
		if callErr2 := json.NewDecoder(r.Body).Decode(&request); callErr2 != nil {
			t.Error(callErr2)
		}
		method, _ := request["method"].(string)
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
			result = JSON{"protocolVersion": "2025-03-26", "capabilities": JSON{}, "serverInfo": JSON{"name": "test", "version": "1"}}
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
	manifest := JSON{"schema": "agenstra.mcp-pack.v1", "name": "metrics", "version": "1", "guidance": "Use metrics", "source": JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{JSON{"name": tool["name"], "effect": "read", "contract_sha256": MCPContractDigest(tool)}}}
	path := writeTestManifest(t, manifest)
	pack, err := OpenMCPPack(context.Background(), path, map[string]string{"MCP_URL": server.URL})
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
	_, err = OpenMCPPack(context.Background(), path, map[string]string{"MCP_URL": server.URL})
	if err == nil || !strings.Contains(err.Error(), "contract changed") {
		t.Fatalf("contract drift accepted: %v", err)
	}
}
