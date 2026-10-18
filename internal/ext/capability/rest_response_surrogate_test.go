package capability

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRESTResponseRejectsUnpairedSurrogatesBeforeProjection(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		version := "v2"
		if legacy {
			version = "legacy"
		}
		for _, tc := range []struct {
			name, payload, id string
			valid             bool
		}{
			{name: "lone high identifier", payload: `{"id":"R\ud8001"}`},
			{name: "lone low identifier", payload: `{"id":"R\udfff1"}`},
			{name: "lone high property", payload: `{"id":"R1","hidden\ud800":0}`},
			{name: "hidden low value", payload: `{"id":"R1","meta":{"value":"\udc00"}}`},
			{name: "array high value", payload: `{"id":"R1","items":["\udbff"]}`},
			{name: "consecutive high units", payload: `{"id":"R\ud800\ud8001"}`},
			{name: "reversed pair", payload: `{"id":"R\udc00\ud8001"}`},
			{name: "second is not low", payload: `{"id":"R\ud800\u00411"}`},
			{name: "literal text after high", payload: `{"id":"R\ud800x1"}`},
			{name: "high at string end", payload: `{"id":"R\udbff"}`},
			{name: "escaped low is literal text", payload: `{"id":"R\ud800\\udc001"}`},
			{name: "extra low after pair", payload: `{"id":"R\ud83d\ude00\udc001"}`},
			{name: "ASCII", payload: `{"id":"R1"}`, id: "R1", valid: true},
			{name: "lowercase pair", payload: `{"id":"R\ud83d\ude001"}`, id: "R😀1", valid: true},
			{name: "uppercase pair", payload: `{"id":"R\uD83D\uDE001"}`, id: "R😀1", valid: true},
			{name: "lowest pair", payload: `{"id":"R\ud800\udc001"}`, id: "R\U000100001", valid: true},
			{name: "highest pair", payload: `{"id":"R\udbff\udfff1"}`, id: "R\U0010ffff1", valid: true},
			{name: "literal replacement", payload: `{"id":"R�1"}`, id: "R�1", valid: true},
			{name: "escaped replacement", payload: `{"id":"R\ufffd1"}`, id: "R�1", valid: true},
			{name: "literal high text", payload: `{"id":"R\\ud8001"}`, id: `R\ud8001`, valid: true},
			{name: "literal low text", payload: `{"id":"R\\udfff1"}`, id: `R\udfff1`, valid: true},
			{name: "escaped slash before pair", payload: `{"id":"R\\\ud83d\ude001"}`, id: "R\\😀1", valid: true},
			{name: "literal surrogate property text", payload: `{"id":"R1","\\ud800":null}`, id: "R1", valid: true},
			{name: "hidden valid pair", payload: `{"id":"记录😀","value":"\ud83d\ude00"}`, id: "记录😀", valid: true},
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

func FuzzRESTResponseUnicodeKeepsEncoderOutput(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("records"), []byte("记录😀�"), []byte(`\ud800`), []byte(`\\ud800\\udc00`), {0xff, 0, '\\', '"'}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		// Arbitrary input must not panic, including truncated escape sequences.
		_ = validRESTResponseJSON(input)
		// The standard encoder produces valid scalar values, including literal
		// escape text and real replacement characters. They must remain accepted.
		encoded, err := json.Marshal(map[string]string{"id": string(input)})
		if err != nil {
			t.Fatal(err)
		}
		if !validRESTResponseJSON(encoded) {
			t.Fatalf("valid encoder output was rejected: %q", encoded)
		}
	})
}
