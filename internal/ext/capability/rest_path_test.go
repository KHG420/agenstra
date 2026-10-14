package capability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestOpenAPIRootAndTrailingSlashReachDeclaredPath(t *testing.T) {
	for _, tc := range []struct {
		path    string
		escaped string
	}{
		{"/", "/"},
		{"/records/", "/records/"},
		{"/records/{record-id}/", "/records/item%20with%20%E6%95%B0%E6%8D%AE/"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if request.URL.EscapedPath() != tc.escaped || request.URL.RawQuery != "" {
					t.Errorf("request changed the declared path: %s", request.URL.String())
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			operation := agentcontract.JSON{
				"operationId": "records.get",
				"responses": agentcontract.JSON{"200": agentcontract.JSON{
					"content": agentcontract.JSON{"application/json": agentcontract.JSON{
						"schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"ok": agentcontract.JSON{"type": "boolean"}}, "required": []any{"ok"}},
					}},
				}},
			}
			arguments := agentcontract.JSON{}
			if tc.path == "/records/{record-id}/" {
				operation["parameters"] = []any{agentcontract.JSON{"name": "record-id", "in": "path", "required": true, "schema": agentcontract.JSON{"type": "string"}}}
				arguments["path"] = agentcontract.JSON{"record-id": "item with 数据"}
			}
			doc := agentcontract.JSON{"openapi": "3.0.3", "paths": agentcontract.JSON{tc.path: agentcontract.JSON{"get": operation}}}
			manifest, err := ImportOpenAPIDocument(doc, "records", "API_URL", []string{"records.get"}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePackManifest(manifest, nil); err != nil {
				t.Fatal(err)
			}
			provider, err := LoadRestPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := provider.Close(); err != nil {
					t.Error(err)
				}
			}()
			result, err := provider.Invoke(context.Background(), "records.get", arguments, nil)
			if err != nil || result.ErrorCode != "" || result.Data["ok"] != true || calls.Load() != 1 {
				t.Fatalf("declared path invocation: %+v, %v, calls=%d", result, err, calls.Load())
			}
			if tc.path == "/records/{record-id}/" {
				for _, unsafe := range []string{"..", "../admin", "x/y", "x?role=admin", "x#fragment", "%2fadmin"} {
					result, err := provider.Invoke(context.Background(), "records.get", agentcontract.JSON{"path": agentcontract.JSON{"record-id": unsafe}}, nil)
					if err != nil || result.ErrorCode != "capability_input_invalid" || calls.Load() != 1 {
						t.Fatalf("unsafe slot reached transport: %+v, %v, calls=%d", result, err, calls.Load())
					}
				}
			}
		})
	}
}

func TestRESTTrailingSlashDoesNotAllowUnsafeSegments(t *testing.T) {
	for _, path := range []string{"", "//", "/records//", "/records//item/", "/./", "/../", "/records/../", "/records/./", "/records/%2f/", "/records/?admin=true", "/records/#fragment", "/records/\\"} {
		t.Run(path, func(t *testing.T) {
			endpoint := agentcontract.RestEndpoint{Name: "records.get", Description: "Read", Method: "GET", Path: path, Effect: "read", InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false}, OutputSchema: agentcontract.JSON{"type": "object"}}
			if err := validateRestEndpoint(endpoint, nil); err == nil {
				t.Fatalf("accepted unsafe path %q", path)
			}
		})
	}
}
