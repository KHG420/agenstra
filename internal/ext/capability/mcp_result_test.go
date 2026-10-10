package capability

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestMCPToolResultErrorFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present bool
		flag    any
		code    string
	}{
		{name: "omitted"},
		{name: "false", present: true, flag: false},
		{name: "true", present: true, flag: true, code: "business_rejected"},
		{name: "string_true", present: true, flag: "true", code: "upstream_response_invalid"},
		{name: "string_false", present: true, flag: "false", code: "upstream_response_invalid"},
		{name: "number_one", present: true, flag: 1, code: "upstream_response_invalid"},
		{name: "number_zero", present: true, flag: 0, code: "upstream_response_invalid"},
		{name: "null", present: true, code: "upstream_response_invalid"},
		{name: "array", present: true, flag: []any{}, code: "upstream_response_invalid"},
		{name: "object", present: true, flag: agentcontract.JSON{}, code: "upstream_response_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := agentcontract.JSON{"name": "record.read", "description": "Read one record", "inputSchema": agentcontract.JSON{"type": "object", "additionalProperties": false}, "outputSchema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var result agentcontract.JSON
				switch request["method"] {
				case "initialize":
					result = agentcontract.JSON{"protocolVersion": "2025-03-26", "capabilities": agentcontract.JSON{}, "serverInfo": agentcontract.JSON{"name": "fixture", "version": "1"}}
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
					return
				case "tools/list":
					result = agentcontract.JSON{"tools": []any{tool}}
				case "tools/call":
					calls++
					data := agentcontract.JSON{"id": "R-1"}
					if tc.flag == true {
						data = agentcontract.JSON{"error": agentcontract.JSON{"code": "business_rejected"}}
					}
					result = agentcontract.JSON{"content": []any{}, "structuredContent": data}
					if tc.present {
						result["isError"] = tc.flag
					}
				default:
					t.Errorf("unexpected MCP method: %v", request["method"])
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			manifest := agentcontract.JSON{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1", "guidance": "Read one record", "source": agentcontract.JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "error_code_path": []any{"error", "code"}, "tools": []any{agentcontract.JSON{"name": tool["name"], "effect": "read", "replay": "safe", "contract_sha256": MCPContractDigest(tool)}}}
			pack, err := OpenMCPPack(t.Context(), writeTestManifest(t, manifest), map[string]string{"MCP_URL": server.URL})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := pack.Close(); err != nil {
					t.Error(err)
				}
			}()
			outcome, err := reactcore.ExecuteCall(t.Context(), pack, map[string]bool{"record.read": true}, agentcontract.ToolCall{Capability: "record.read", Arguments: agentcontract.JSON{}}, nil)
			if err != nil || calls != 1 || outcome.ErrorCode != tc.code {
				t.Fatalf("tool result classified incorrectly: %+v, %v, calls=%d", outcome, err, calls)
			}
			if tc.code != "" && outcome.Fact != nil {
				t.Fatal("failed or malformed receipt became a success Fact")
			}
			if tc.code == "" && (outcome.Fact == nil || outcome.Fact.Value["data"].(map[string]any)["id"] != "R-1") {
				t.Fatal("valid success receipt lost its record")
			}
		})
	}
}
