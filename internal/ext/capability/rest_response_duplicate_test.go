package capability

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRESTResponseRejectsDuplicatePropertiesBeforeProjection(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		version := "v2"
		if legacy {
			version = "legacy"
		}
		for _, tc := range []struct {
			name, payload string
			valid         bool
		}{
			{"different root values", `{"id":"R1","id":"R2"}`, false},
			{"identical root values", `{"id":"R1","id":"R1"}`, false},
			{"escaped property", `{"id":"R1","\u0069d":"R2"}`, false},
			{"business status", `{"id":"R1","code":400,"code":0}`, false},
			{"nested hidden property", `{"id":"R1","meta":{"state":"error","state":"active"}}`, false},
			{"array member property", `{"id":"R1","items":[{"value":0,"value":1}]}`, false},
			{"independent objects", `{"id":"R1","meta":{"id":"M1"}}`, true},
			{"independent array members", `{"id":"R1","items":[{"id":"A"},{"id":"B"}]}`, true},
			{"string value matches property", `{"id":"R1","meta":{"name":"id","id":"M1"}}`, true},
			{"nested arrays and objects", `{"id":"R1","items":[[],{"items":[{"id":"A"}],"id":"B"}],"meta":{}}`, true},
			{"precise numbers", `{"id":"R1","n":9007199254740993.0001,"tiny":1e-400}`, true},
		} {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests++
					if _, err := w.Write([]byte(tc.payload)); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				var manifest agentcontract.JSON
				if legacy {
					manifest = agentcontract.JSON{"schema": "agenstra.capability-pack.v1", "name": "records", "guidance": "Read original records",
						"capabilities": []any{agentcontract.JSON{"name": "record.read", "version": "1", "description": "Read a record", "method": "GET", "url_env": "API",
							"inputs": agentcontract.JSON{}, "outputs": agentcontract.JSON{"id": agentcontract.JSON{"type": "string", "description": "Record ID"}}}}}
				} else {
					manifest = agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Read original records", "base_url_env": "API",
						"capabilities": []any{agentcontract.JSON{"name": "record.read", "description": "Read a record", "method": "GET", "path": "/record", "effect": "read",
							"input_schema":  agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
							"output_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []string{"id"}},
						}}}
				}
				path := writeTestManifest(t, manifest)
				var pack agentcontract.CapabilityProvider
				var err error
				if legacy {
					pack, err = LoadLegacyPack(path, map[string]string{"API": server.URL}, server.Client())
				} else {
					pack, err = LoadRestPack(path, map[string]string{"API": server.URL}, server.Client())
				}
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := pack.Close(); err != nil {
						t.Error(err)
					}
				}()
				result, err := pack.Invoke(t.Context(), "record.read", agentcontract.JSON{}, nil)
				if err != nil || requests != 1 {
					t.Fatalf("requests=%d error=%v", requests, err)
				}
				if !tc.valid {
					if result.ErrorCode != "upstream_response_invalid" || result.Data != nil {
						t.Fatalf("ambiguous response survived validation or projection: %+v", result)
					}
					return
				}
				if result.ErrorCode != "" || result.Data["id"] != "R1" {
					t.Fatalf("unique extension fields or scopes rejected: %+v", result)
				}
				if !legacy && tc.name == "precise numbers" && (result.Data["n"] != json.Number("9007199254740993.0001") || result.Data["tiny"] != json.Number("1e-400")) {
					t.Fatal("response number representations changed", result.Data)
				}
			})
		}
	}
}
