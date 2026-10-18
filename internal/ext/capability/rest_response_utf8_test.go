package capability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRESTResponseRejectsInvalidUTF8BeforeProjection(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		version := "v2"
		if legacy {
			version = "legacy"
		}
		for _, tc := range []struct {
			name, payload, id string
			valid             bool
		}{
			{name: "invalid identifier", payload: "{\"id\":\"R\xff1\"}"},
			{name: "invalid property name", payload: "{\"id\":\"R1\",\"hidden\xff\":0}"},
			{name: "hidden nested value", payload: "{\"id\":\"R1\",\"meta\":{\"value\":\"\x80\"}}"},
			{name: "array value", payload: "{\"id\":\"R1\",\"items\":[\"\xff\"]}"},
			{name: "overlong sequence", payload: "{\"id\":\"R1\",\"value\":\"\xc0\xaf\"}"},
			{name: "truncated sequence", payload: "{\"id\":\"R1\",\"value\":\"\xe4\xb8\"}"},
			{name: "encoded surrogate", payload: "{\"id\":\"R1\",\"value\":\"\xed\xa0\x80\"}"},
			{name: "outside Unicode range", payload: "{\"id\":\"R1\",\"value\":\"\xf4\x90\x80\x80\"}"},
			{name: "ASCII", payload: `{"id":"R1"}`, id: "R1", valid: true},
			{name: "Chinese and emoji", payload: `{"id":"记录😀"}`, id: "记录😀", valid: true},
			{name: "literal replacement character", payload: `{"id":"R�1"}`, id: "R�1", valid: true},
			{name: "escaped replacement character", payload: `{"id":"R\ufffd1"}`, id: "R�1", valid: true},
			{name: "escaped supplementary character", payload: `{"id":"R\ud83d\ude001"}`, id: "R😀1", valid: true},
		} {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests++
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
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
						t.Fatalf("invalid response bytes became decoded business data: %+v", result)
					}
					return
				}
				if result.ErrorCode != "" || result.Data["id"] != tc.id {
					t.Fatalf("valid Unicode response changed: %+v", result)
				}
			})
		}
	}
}
