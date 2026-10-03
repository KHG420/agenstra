package agenstra

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestOpenAPIUnionAndComposedResponsesExecuteTheirDeclaredContract(t *testing.T) {
	object := JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}
	for _, tc := range []struct {
		name, version string
		schema        JSON
		components    JSON
		payloads      []string
		invalid       string
		wrapped       bool
	}{
		{"nullable-object", "3.0.3", JSON{"type": "object", "nullable": true, "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}}, nil, []string{`null`, `{"id":"R1"}`}, `1`, true},
		{"nullable-reference", "3.0.3", JSON{"$ref": "#/components/schemas/Record", "nullable": true}, JSON{"Record": object}, []string{`null`, `{"id":"R1"}`}, `1`, true},
		{"nullable-scalar", "3.0.3", JSON{"type": "integer", "nullable": true}, nil, []string{`null`, `2`}, `"invalid"`, true},
		{"type-union", "3.1.0", JSON{"type": []any{"object", "null"}, "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}}, nil, []string{`null`, `{"id":"R1"}`}, `1`, true},
		{"oneof-mixed", "3.1.0", JSON{"oneOf": []any{JSON{"type": "integer"}, JSON{"type": "array", "items": JSON{"type": "string"}}}}, nil, []string{`2`, `["A"]`}, `true`, true},
		{"anyof-reference", "3.1.0", JSON{"anyOf": []any{JSON{"$ref": "#/components/schemas/Record"}, JSON{"type": "null"}}}, JSON{"Record": object}, []string{`null`, `{"id":"R1"}`}, `1`, true},
		{"allof-scalar", "3.1.0", JSON{"allOf": []any{JSON{"type": "integer"}, JSON{"minimum": 1}}}, nil, []string{`2`}, `0`, true},
		{"reference-siblings", "3.1.0", JSON{"$ref": "#/components/schemas/Count", "minimum": 1}, JSON{"Count": JSON{"type": "integer"}}, []string{`2`}, `0`, true},
		{"plain-object", "3.1.0", object, nil, []string{`{"id":"R1"}`}, `null`, false},
		{"allof-object", "3.1.0", JSON{"allOf": []any{JSON{"$ref": "#/components/schemas/Record"}, JSON{"description": "Record result"}}}, JSON{"Record": object}, []string{`{"id":"R1"}`}, `null`, false},
		{"anyof-objects", "3.1.0", JSON{"anyOf": []any{object, JSON{"type": "object", "required": []any{"other"}}}}, nil, []string{`{"id":"R1"}`, `{"other":true}`}, `null`, false},
		{"oneof-objects", "3.1.0", JSON{"oneOf": []any{object, JSON{"type": "object", "required": []any{"other"}}}}, nil, []string{`{"id":"R1"}`, `{"other":true}`}, `null`, false},
		{"object-type-list", "3.1.0", JSON{"type": []any{"object"}, "properties": object["properties"], "required": object["required"]}, nil, []string{`{"id":"R1"}`}, `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(payload))
			}))
			defer server.Close()
			doc := JSON{"openapi": tc.version, "components": JSON{"schemas": tc.components}, "paths": JSON{"/value": JSON{"get": JSON{
				"operationId": "getValue", "responses": JSON{"200": JSON{"content": JSON{"application/json": JSON{"schema": tc.schema}}}},
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
			defer pack.Close()
			for _, payload = range tc.payloads {
				var value any
				decoder := json.NewDecoder(bytes.NewBufferString(payload))
				decoder.UseNumber()
				if err := decoder.Decode(&value); err != nil {
					t.Fatal(err)
				}
				want := value
				if tc.wrapped {
					want = JSON{"result": value}
				}
				result, err := pack.Invoke(t.Context(), "getValue", JSON{}, nil)
				if err != nil || result.ErrorCode != "" || !reflect.DeepEqual(result.Data, want) {
					t.Fatalf("declared response %s: result=%+v err=%v want=%v", payload, result, err, want)
				}
			}
			payload = tc.invalid
			result, err := pack.Invoke(t.Context(), "getValue", JSON{}, nil)
			if err != nil || result.ErrorCode != "upstream_response_invalid" {
				t.Fatalf("response outside original schema accepted: %+v %v", result, err)
			}
		})
	}
}
