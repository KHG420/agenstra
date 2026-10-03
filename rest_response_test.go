package agenstra

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func responseTestPack(t *testing.T, legacy bool, server *httptest.Server) CapabilityProvider {
	t.Helper()
	var manifest JSON
	if legacy {
		manifest = JSON{"schema": "agenstra.capability-pack.v1", "name": "records", "guidance": "Read records", "capabilities": []any{JSON{
			"name": "record.read", "version": "1", "description": "Read record", "method": "GET", "url_env": "API_URL",
			"inputs": JSON{}, "outputs": JSON{"id": JSON{"type": "string", "description": "Record ID"}},
		}}}
		pack, err := LoadLegacyPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := pack.Close(); err != nil {
				t.Error(err)
			}
		})
		return pack
	}
	manifest = JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Read records", "base_url_env": "API_URL", "capabilities": []any{JSON{
		"name": "record.read", "description": "Read record", "method": "GET", "path": "/records", "effect": "read",
		"input_schema":  JSON{"type": "object", "properties": JSON{}, "additionalProperties": false},
		"output_schema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false},
	}}}
	pack, err := LoadRestPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pack.Close(); err != nil {
			t.Error(err)
		}
	})
	return pack
}

func TestRESTResponseRequiresOneCompleteBoundedJSONValue(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		version := "v2"
		if legacy {
			version = "v1"
		}
		for _, tc := range []struct {
			name, payload, code string
		}{
			{"valid", `{"id":"R1"}`, ""},
			{"whitespace", "{\"id\":\"R1\"}\n \t", ""},
			{"size-limit", `{"id":"R1"}` + strings.Repeat(" ", (16<<20)-len(`{"id":"R1"}`)), ""},
			{"second-value", `{"id":"R1"} {"id":"R2"}`, "upstream_response_invalid"},
			{"trailing-garbage", `{"id":"R1"} incomplete`, "upstream_response_invalid"},
			{"incomplete-value", `{"id":"R1"`, "upstream_response_invalid"},
			{"oversized", `{"id":"R1"}` + strings.Repeat(" ", (16<<20)+1-len(`{"id":"R1"}`)), "upstream_response_invalid"},
		} {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if _, callErr := w.Write([]byte(tc.payload)); callErr != nil {
						t.Error(callErr)
					}
				}))
				defer server.Close()
				pack := responseTestPack(t, legacy, server)
				result, err := pack.Invoke(t.Context(), "record.read", JSON{}, nil)
				if err != nil || result.ErrorCode != tc.code {
					t.Fatalf("result=%+v err=%v; want code %q", result, err, tc.code)
				}
				if tc.code == "" && result.Data["id"] != "R1" {
					t.Fatalf("valid response lost: %+v", result)
				}
			})
		}
	}
}

func TestLegacyRESTRejectsNonSuccessHTTPStatus(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusNotModified} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				if status == http.StatusNotModified {
					return // HTTP 304 responses cannot carry a body.
				}
				if _, callErr2 := w.Write([]byte(`{"id":"R1"}`)); callErr2 != nil {
					t.Error(callErr2)
				}
			}))
			defer server.Close()
			pack := responseTestPack(t, true, server)
			result, err := pack.Invoke(context.Background(), "record.read", JSON{}, nil)
			if err != nil || !strings.HasPrefix(result.ErrorCode, "upstream_http_") || result.Data != nil {
				t.Fatalf("non-success status became a business result: %+v %v", result, err)
			}
		})
	}
}

func TestRESTMalformedWriteResponsePausesWithoutReplayingBusinessOperation(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		writes.Add(1)
		if _, callErr3 := w.Write([]byte(`{"id":"R1"} {"id":"R2"}`)); callErr3 != nil {
			t.Error(callErr3)
		}
	}))
	defer server.Close()
	manifest := JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Create records", "base_url_env": "API_URL", "capabilities": []any{JSON{
		"name": "record.create", "description": "Create record", "method": "POST", "path": "/records", "effect": "write",
		"input_schema":  JSON{"type": "object", "properties": JSON{}, "additionalProperties": false},
		"output_schema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false},
	}}}
	pack, err := LoadRestPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
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
	run, err := host.Create(t.Context(), "alice", "records", "Create one record", "malformed-write")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		run, err = host.Drive(t.Context(), run.RunID, "alice")
		if err != nil || run.Status != "needs_reconciliation" {
			t.Fatalf("malformed write result was treated as confirmed: %+v %v", run, err)
		}
		if writes.Load() != 1 {
			t.Fatalf("business operation replayed: %d", writes.Load())
		}
	}
}
