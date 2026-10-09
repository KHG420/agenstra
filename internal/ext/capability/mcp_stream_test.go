package capability

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestMCPPackConnectsAndInvokesUsingMultilineSSE(t *testing.T) {
	tool := agentcontract.JSON{"name": "record.read", "description": "Read record", "inputSchema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false}, "outputSchema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request agentcontract.JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request["method"] == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result agentcontract.JSON
		switch request["method"] {
		case "initialize":
			result = agentcontract.JSON{"protocolVersion": "2025-03-26", "capabilities": agentcontract.JSON{}, "serverInfo": agentcontract.JSON{"name": "test", "version": "1"}}
		case "tools/list":
			result = agentcontract.JSON{"tools": []any{tool}}
		case "tools/call":
			result = agentcontract.JSON{"content": []any{}, "structuredContent": agentcontract.JSON{"id": "R1"}}
		default:
			t.Errorf("unexpected method: %v", request["method"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if _, callErr5 := fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n"); callErr5 != nil {
			t.Error(callErr5)
		}
		raw, err := json.MarshalIndent(agentcontract.JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}, "", "  ")
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
	manifest := agentcontract.JSON{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1", "guidance": "Read records", "source": agentcontract.JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{agentcontract.JSON{"name": tool["name"], "effect": "read", "contract_sha256": MCPContractDigest(tool)}}}
	pack, err := OpenMCPPack(t.Context(), writeTestManifest(t, manifest), map[string]string{"MCP_URL": server.URL})
	if err != nil {
		t.Fatalf("valid streaming server cannot be connected: %v", err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(pack.Close)
	result, err := pack.Invoke(t.Context(), "record.read", agentcontract.JSON{}, nil)
	if err != nil || result.ErrorCode != "" || result.Data["id"] != "R1" {
		t.Fatalf("streaming business result lost: %+v %v", result, err)
	}
}
