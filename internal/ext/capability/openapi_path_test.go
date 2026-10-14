package capability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestOpenAPIHyphenatedPathParameterPreservesNameAndEscaping(t *testing.T) {
	const value = "item with 数据"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.EscapedPath() != "/records/item%20with%20%E6%95%B0%E6%8D%AE" || request.URL.RawQuery != "" {
			t.Errorf("path value escaped its declared slot: %s", request.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"id":"item with 数据"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	doc := agentcontract.JSON{
		"openapi": "3.0.3",
		"paths": agentcontract.JSON{
			"/records/{record-id}": agentcontract.JSON{
				"get": agentcontract.JSON{
					"operationId": "records.get",
					"parameters": []any{agentcontract.JSON{
						"name": "record-id", "in": "path", "required": true,
						"schema": agentcontract.JSON{"type": "string"},
					}},
					"responses": agentcontract.JSON{"200": agentcontract.JSON{
						"content": agentcontract.JSON{"application/json": agentcontract.JSON{
							"schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}},
						}},
					}},
				},
			},
		},
	}
	draft, err := ImportOpenAPIDocument(doc, "records", "API_URL", []string{"records.get"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePackManifest(draft, nil); err != nil {
		t.Fatal(err)
	}
	provider, err := LoadRestPack(writeTestManifest(t, draft), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := provider.Invoke(context.Background(), "records.get", agentcontract.JSON{"path": agentcontract.JSON{"record-id": value}}, nil)
	if err != nil || result.ErrorCode != "" || result.Data["id"] != value {
		t.Fatalf("hyphenated path result: %+v, %v", result, err)
	}
	for _, unsafe := range []string{"..", "../admin", "x/y", "x?role=admin", "x#fragment", "%2fadmin"} {
		result, err := provider.Invoke(context.Background(), "records.get", agentcontract.JSON{"path": agentcontract.JSON{"record-id": unsafe}}, nil)
		if err != nil || result.ErrorCode != "capability_input_invalid" || calls.Load() != 1 {
			t.Fatalf("unsafe value reached transport: %+v, %v", result, err)
		}
	}
}
