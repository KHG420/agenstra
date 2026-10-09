package capability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func responseTestPack(t *testing.T, legacy bool, server *httptest.Server) agentcontract.CapabilityProvider {
	t.Helper()
	var manifest agentcontract.JSON
	if legacy {
		manifest = agentcontract.JSON{"schema": "agenstra.capability-pack.v1", "name": "records", "guidance": "Read records", "capabilities": []any{agentcontract.JSON{
			"name": "record.read", "version": "1", "description": "Read record", "method": "GET", "url_env": "API_URL",
			"inputs": agentcontract.JSON{}, "outputs": agentcontract.JSON{"id": agentcontract.JSON{"type": "string", "description": "Record ID"}},
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
	manifest = agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Read records", "base_url_env": "API_URL", "capabilities": []any{agentcontract.JSON{
		"name": "record.read", "description": "Read record", "method": "GET", "path": "/records", "effect": "read",
		"input_schema":  agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
		"output_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false},
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
				result, err := pack.Invoke(t.Context(), "record.read", agentcontract.JSON{}, nil)
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
			result, err := pack.Invoke(context.Background(), "record.read", agentcontract.JSON{}, nil)
			if err != nil || !strings.HasPrefix(result.ErrorCode, "upstream_http_") || result.Data != nil {
				t.Fatalf("non-success status became a business result: %+v %v", result, err)
			}
		})
	}
}
