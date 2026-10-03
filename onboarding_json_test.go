package agenstra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func TestDirectPackLoadersRequireOneCompleteJSONDocument(t *testing.T) {
	tool := JSON{"name": "record.read", "description": "Read record", "inputSchema": JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}, "outputSchema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}}}
	for _, kind := range []string{"rest-v2", "rest-v1", "mcp-v1"} {
		for _, tc := range []struct {
			name, suffix string
			valid        bool
		}{
			{"single-document", "", true},
			{"trailing-whitespace", "\n \t", true},
			{"second-document", ` {"schema":"different"}`, false},
			{"trailing-garbage", ` invalid`, false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					var request JSON
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					var result JSON
					switch request["method"] {
					case "initialize":
						result = JSON{"protocolVersion": "2025-03-26", "capabilities": JSON{}, "serverInfo": JSON{"name": "test", "version": "1"}}
					case "notifications/initialized":
						w.WriteHeader(http.StatusAccepted)
						return
					case "tools/list":
						result = JSON{"tools": []any{tool}}
					default:
						t.Errorf("unexpected method: %v", request["method"])
					}
					w.Header().Set("Content-Type", "application/json")
					if callErr := json.NewEncoder(w).Encode(JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}); callErr != nil {
						t.Error(callErr)
					}
				}))
				defer server.Close()
				var manifest JSON
				var load func(context.Context, string, map[string]string) (CapabilityProvider, error)
				switch kind {
				case "rest-v2":
					manifest = JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Read records", "base_url_env": "API_URL", "capabilities": []any{JSON{"name": "record.read", "description": "Read record", "method": "GET", "path": "/records", "effect": "read", "input_schema": tool["inputSchema"], "output_schema": tool["outputSchema"]}}}
					load = func(_ context.Context, path string, env map[string]string) (CapabilityProvider, error) {
						return LoadRestPack(path, env, server.Client())
					}
				case "rest-v1":
					manifest = JSON{"schema": "agenstra.capability-pack.v1", "name": "records", "guidance": "Read records", "capabilities": []any{JSON{"name": "record.read", "description": "Read record", "version": "1", "method": "GET", "url_env": "API_URL", "inputs": JSON{}, "outputs": JSON{"id": JSON{"type": "string", "description": "Record ID"}}}}}
					load = func(_ context.Context, path string, env map[string]string) (CapabilityProvider, error) {
						return LoadLegacyPack(path, env, server.Client())
					}
				case "mcp-v1":
					manifest = JSON{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1", "guidance": "Read records", "source": JSON{"transport": "streamable_http", "url_env": "API_URL"}, "tools": []any{JSON{"name": tool["name"], "effect": "read", "contract_sha256": MCPContractDigest(tool)}}}
					load = func(ctx context.Context, path string, env map[string]string) (CapabilityProvider, error) {
						return OpenMCPPack(ctx, path, env)
					}
				}
				path := writeTestManifest(t, manifest)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(raw, []byte(tc.suffix)...), 0600); err != nil {
					t.Fatal(err)
				}
				pack, err := load(t.Context(), path, map[string]string{"API_URL": server.URL})
				if err == nil {
					defer func(close func() error) {
						if err := close(); err != nil {
							t.Error(err)
						}
					}(pack.Close)
				}
				if tc.valid {
					if err != nil || len(pack.Capabilities()) != 1 {
						t.Fatalf("valid document rejected: %v", err)
					}
				} else if err == nil || requests.Load() != 0 {
					t.Fatalf("invalid document accepted or connected externally: err=%v requests=%d", err, requests.Load())
				}
			})
		}
	}
}

func TestOpenAPIFileImportRejectsTrailingDocumentsAndGarbage(t *testing.T) {
	doc := JSON{"openapi": "3.1.0", "paths": JSON{"/records": JSON{"get": JSON{"operationId": "getRecords", "responses": JSON{"200": JSON{"content": JSON{"application/json": JSON{"schema": JSON{"type": "object"}}}}}}}}}
	for _, tc := range []struct {
		name, suffix string
		valid        bool
	}{
		{"single-document", "", true},
		{"trailing-whitespace", "\n \t", true},
		{"second-document", ` {"openapi":"different"}`, false},
		{"trailing-garbage", ` invalid`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestManifest(t, doc)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(raw, []byte(tc.suffix)...), 0600); err != nil {
				t.Fatal(err)
			}
			draft, err := ImportOpenAPI(path, "records", "API_URL", []string{"getRecords"}, nil, "")
			if tc.valid {
				if err != nil || len(draft["capabilities"].([]any)) != 1 {
					t.Fatalf("valid OpenAPI file rejected: %v", err)
				}
			} else if err == nil {
				t.Fatalf("invalid OpenAPI file silently imported: %v", draft)
			}
		})
	}
}
