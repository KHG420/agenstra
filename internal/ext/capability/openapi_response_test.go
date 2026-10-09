package capability

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestOpenAPIUnionAndComposedResponsesExecuteTheirDeclaredContract(t *testing.T) {
	object := agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}
	for _, tc := range []struct {
		name, version string
		schema        agentcontract.JSON
		components    agentcontract.JSON
		payloads      []string
		invalid       string
		wrapped       bool
	}{
		{"nullable-object", "3.0.3", agentcontract.JSON{"type": "object", "nullable": true, "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}}, nil, []string{`null`, `{"id":"R1"}`}, `1`, true},
		{"nullable-reference", "3.0.3", agentcontract.JSON{"$ref": "#/components/schemas/Record", "nullable": true}, agentcontract.JSON{"Record": object}, []string{`null`, `{"id":"R1"}`}, `1`, true},
		{"nullable-scalar", "3.0.3", agentcontract.JSON{"type": "integer", "nullable": true}, nil, []string{`null`, `2`}, `"invalid"`, true},
		{"type-union", "3.1.0", agentcontract.JSON{"type": []any{"object", "null"}, "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}}, nil, []string{`null`, `{"id":"R1"}`}, `1`, true},
		{"oneof-mixed", "3.1.0", agentcontract.JSON{"oneOf": []any{agentcontract.JSON{"type": "integer"}, agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "string"}}}}, nil, []string{`2`, `["A"]`}, `true`, true},
		{"anyof-reference", "3.1.0", agentcontract.JSON{"anyOf": []any{agentcontract.JSON{"$ref": "#/components/schemas/Record"}, agentcontract.JSON{"type": "null"}}}, agentcontract.JSON{"Record": object}, []string{`null`, `{"id":"R1"}`}, `1`, true},
		{"allof-scalar", "3.1.0", agentcontract.JSON{"allOf": []any{agentcontract.JSON{"type": "integer"}, agentcontract.JSON{"minimum": 1}}}, nil, []string{`2`}, `0`, true},
		{"reference-siblings", "3.1.0", agentcontract.JSON{"$ref": "#/components/schemas/Count", "minimum": 1}, agentcontract.JSON{"Count": agentcontract.JSON{"type": "integer"}}, []string{`2`}, `0`, true},
		{"plain-object", "3.1.0", object, nil, []string{`{"id":"R1"}`}, `null`, false},
		{"allof-object", "3.1.0", agentcontract.JSON{"allOf": []any{agentcontract.JSON{"$ref": "#/components/schemas/Record"}, agentcontract.JSON{"description": "Record result"}}}, agentcontract.JSON{"Record": object}, []string{`{"id":"R1"}`}, `null`, false},
		{"anyof-objects", "3.1.0", agentcontract.JSON{"anyOf": []any{object, agentcontract.JSON{"type": "object", "required": []any{"other"}}}}, nil, []string{`{"id":"R1"}`, `{"other":true}`}, `null`, false},
		{"oneof-objects", "3.1.0", agentcontract.JSON{"oneOf": []any{object, agentcontract.JSON{"type": "object", "required": []any{"other"}}}}, nil, []string{`{"id":"R1"}`, `{"other":true}`}, `null`, false},
		{"object-type-list", "3.1.0", agentcontract.JSON{"type": []any{"object"}, "properties": object["properties"], "required": object["required"]}, nil, []string{`{"id":"R1"}`}, `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if _, callErr := w.Write([]byte(payload)); callErr != nil {
					t.Error(callErr)
				}
			}))
			defer server.Close()
			doc := agentcontract.JSON{"openapi": tc.version, "components": agentcontract.JSON{"schemas": tc.components}, "paths": agentcontract.JSON{"/value": agentcontract.JSON{"get": agentcontract.JSON{
				"operationId": "getValue", "responses": agentcontract.JSON{"200": agentcontract.JSON{"content": agentcontract.JSON{"application/json": agentcontract.JSON{"schema": tc.schema}}}},
			}}}}
			draft, err := ImportOpenAPIDocument(doc, "values", "API_URL", []string{"getValue"}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePackManifest(draft, nil); err != nil {
				t.Fatalf("imported contract cannot be published: %v", err)
			}
			pack, err := LoadRestPack(writeTestManifest(t, draft), map[string]string{"API_URL": server.URL}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer func(close func() error) {
				if err := close(); err != nil {
					t.Error(err)
				}
			}(pack.Close)
			for _, payload = range tc.payloads {
				var value any
				decoder := json.NewDecoder(bytes.NewBufferString(payload))
				decoder.UseNumber()
				if err := decoder.Decode(&value); err != nil {
					t.Fatal(err)
				}
				want := value
				if tc.wrapped {
					want = agentcontract.JSON{"result": value}
				}
				result, err := pack.Invoke(t.Context(), "getValue", agentcontract.JSON{}, nil)
				if err != nil || result.ErrorCode != "" || !reflect.DeepEqual(result.Data, want) {
					t.Fatalf("declared response %s: result=%+v err=%v want=%v", payload, result, err, want)
				}
			}
			payload = tc.invalid
			result, err := pack.Invoke(t.Context(), "getValue", agentcontract.JSON{}, nil)
			if err != nil || result.ErrorCode != "upstream_response_invalid" {
				t.Fatalf("response outside original schema accepted: %+v %v", result, err)
			}
		})
	}
}
