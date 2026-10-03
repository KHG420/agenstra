package agenstra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTestManifest(t *testing.T, v any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pack.json")
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestRESTValidationTransportAndOutput(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "PATCH" || r.URL.EscapedPath() != "/records/R%201" || r.URL.Query()["tag"][1] != "priority" || (calls == 1 && (r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Tenant") != "tenant")) || r.Header.Get("Idempotency-Key") != "idem" {
			t.Errorf("request boundary: %+v", r)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, callErr := w.Write([]byte(`{"record":{"id":"R 1"}}`)); callErr != nil {
			t.Error(callErr)
		}
	}))
	defer server.Close()
	endpoint := JSON{"name": "record.update", "description": "Update", "method": "PATCH", "path": "/records/{record_id}", "effect": "write", "idempotency_header": "Idempotency-Key", "response_path": []any{"record"}, "input_schema": JSON{"type": "object", "additionalProperties": false, "properties": JSON{"path": JSON{"type": "object", "properties": JSON{"record_id": JSON{"type": "string"}}, "required": []any{"record_id"}, "additionalProperties": false}, "query": JSON{"type": "object", "properties": JSON{"tag": JSON{"type": "array", "items": JSON{"type": "string"}}}, "additionalProperties": false}, "body": JSON{"type": "object", "properties": JSON{"profile": JSON{"type": "string"}}, "required": []any{"profile"}, "additionalProperties": false}}, "required": []any{"path", "body"}}, "output_schema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}, "response_schemas": JSON{"200": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}}
	path := writeTestManifest(t, JSON{"schema": "agenstra.rest-pack.v2", "name": "record", "version": "1.0", "guidance": "Use records", "base_url_env": "API_URL", "token_env": "API_TOKEN", "headers_env": JSON{"X-Tenant": "TENANT"}, "capabilities": []any{endpoint}})
	pack, err := LoadRestPack(path, map[string]string{"API_URL": server.URL, "API_TOKEN": "secret", "TENANT": "tenant"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	invalid := JSON{"path": JSON{"record_id": "R 1", "extra": "unsafe"}, "body": JSON{"profile": "x"}}
	result, err := pack.Invoke(context.Background(), "record.update", invalid, nil)
	if err != nil || result.ErrorCode != "capability_input_invalid" || calls != 0 {
		t.Fatalf("invalid input hit server: %+v %v", result, err)
	}
	args := JSON{"path": JSON{"record_id": "R 1"}, "query": JSON{"tag": []any{"north", "priority"}}, "body": JSON{"profile": "x"}}
	result, err = pack.Invoke(context.Background(), "record.update", args, &InvocationContext{IdempotencyKey: "idem"})
	if err != nil || result.Data["id"] != "R 1" || calls != 1 {
		t.Fatalf("result: %+v %v calls=%d", result, err, calls)
	}
	endpoint["output_schema"] = JSON{"type": "object", "properties": JSON{"id": JSON{"type": "integer"}}, "required": []any{"id"}, "additionalProperties": false}
	path = writeTestManifest(t, JSON{"schema": "agenstra.rest-pack.v2", "name": "record", "version": "1.0", "guidance": "Use records", "base_url_env": "API_URL", "capabilities": []any{endpoint}})
	pack, err = LoadRestPack(path, map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err = pack.Invoke(context.Background(), "record.update", args, &InvocationContext{IdempotencyKey: "idem"})
	if err != nil || result.ErrorCode != "upstream_response_invalid" {
		t.Fatalf("output boundary: %+v %v", result, err)
	}
}
func TestRESTRejectsUnsafeBindingAndUnresolvedSchema(t *testing.T) {
	base := JSON{"schema": "agenstra.rest-pack.v2", "name": "x", "version": "1", "guidance": "x", "base_url_env": "URL", "capabilities": []any{JSON{"name": "records.read", "description": "Read", "method": "GET", "path": "/records", "effect": "read", "input_schema": JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}, "output_schema": JSON{"type": "object"}}}}
	endpoint := base["capabilities"].([]any)[0].(map[string]any)
	endpoint["path"] = "/records/../private"
	if err := ValidatePackManifest(base, nil); err == nil {
		t.Fatal("accepted traversal")
	}
	endpoint["path"] = "/records"
	endpoint["input_schema"].(map[string]any)["properties"] = JSON{"query": JSON{"type": "object", "properties": JSON{"id": JSON{"$ref": "https://remote/schema"}}, "additionalProperties": false}}
	if err := ValidatePackManifest(base, nil); err == nil || !strings.Contains(err.Error(), "local") {
		t.Fatalf("external ref accepted: %v", err)
	}
}

func TestLegacyRESTOptionalNullAndOutputFiltering(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload JSON
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if _, exists := payload["note"]; exists {
			t.Error("optional null input should be omitted")
		}
		if _, callErr2 := w.Write([]byte(`{"record":{"id":"R1","note":null,"secret":"not exposed"}}`)); callErr2 != nil {
			t.Error(callErr2)
		}
	}))
	defer server.Close()
	manifest := JSON{"schema": "agenstra.capability-pack.v1", "name": "legacy", "guidance": "Use records", "capabilities": []any{JSON{"name": "record.read", "version": "1", "description": "Read", "url_env": "API_URL", "inputs": JSON{"id": JSON{"type": "string", "description": "ID"}, "note": JSON{"type": "string", "description": "Optional", "required": false}}, "outputs": JSON{"id": JSON{"type": "string", "description": "ID"}, "note": JSON{"type": "string", "description": "Optional", "required": false}}, "response_path": []any{"record"}}}}
	path := writeTestManifest(t, manifest)
	pack, err := LoadLegacyPack(path, map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := pack.Invoke(context.Background(), "record.read", JSON{"id": "R1", "note": nil}, nil)
	if err != nil || result.Data["id"] != "R1" || len(result.Data) != 1 || requests != 1 {
		t.Fatalf("legacy optional/output: %+v err=%v requests=%d", result, err, requests)
	}
}

func TestRESTSchemaChecksOnlySchemaPositions(t *testing.T) {
	literal := JSON{"$ref": "provider-data", "$id": "provider-id", "$schema": "provider-metadata"}
	schema := JSON{"type": "object", "properties": JSON{
		"$ref":     JSON{"type": "string"},
		"metadata": JSON{"enum": []any{literal}, "default": literal},
	}}
	if _, err := validateLocalSchema(schema, true); err != nil {
		t.Fatalf("literal JSON treated as a schema instruction: %v", err)
	}
	for _, nested := range []JSON{
		{"properties": JSON{"item": JSON{"$ref": "https://external.invalid/schema"}}},
		{"items": JSON{"$ref": "https://external.invalid/schema"}},
		{"allOf": []any{JSON{"$ref": "https://external.invalid/schema"}}},
		{"additionalProperties": JSON{"$id": "https://external.invalid/schema"}},
	} {
		if _, err := validateLocalSchema(nested, true); err == nil {
			t.Fatalf("unsafe nested schema accepted: %v", nested)
		}
	}
}

func TestRESTExplicitResponseNormalizationAndBusinessFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/array":
			if _, callErr3 := w.Write([]byte(`["A","B"]`)); callErr3 != nil {
				t.Error(callErr3)
			}
		case "/scalar":
			if _, callErr4 := w.Write([]byte(`17`)); callErr4 != nil {
				t.Error(callErr4)
			}
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		case "/empty-200":
			w.WriteHeader(http.StatusOK)
		case "/business-failed":
			if _, callErr5 := w.Write([]byte(`{"ok":false,"record":{"id":"R1"}}`)); callErr5 != nil {
				t.Error(callErr5)
			}
		case "/business-missing":
			if _, callErr6 := w.Write([]byte(`{"record":{"id":"R1"}}`)); callErr6 != nil {
				t.Error(callErr6)
			}
		case "/business-ok":
			if _, callErr7 := w.Write([]byte(`{"ok":true,"record":{"id":"R1"}}`)); callErr7 != nil {
				t.Error(callErr7)
			}
		default:
			if _, callErr8 := w.Write([]byte(`{"id":"R1"}`)); callErr8 != nil {
				t.Error(callErr8)
			}
		}
	}))
	defer server.Close()
	input := JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}
	wrappedArray := JSON{"type": "object", "properties": JSON{"result": JSON{"type": "array", "items": JSON{"type": "string"}}}, "required": []any{"result"}, "additionalProperties": false}
	wrappedScalar := JSON{"type": "object", "properties": JSON{"result": JSON{"type": "integer"}}, "required": []any{"result"}, "additionalProperties": false}
	empty := JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}
	object := JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}
	base := func(name, path string, output JSON) JSON {
		return JSON{"name": name, "description": name, "method": "GET", "path": path, "effect": "read", "input_schema": input, "output_schema": output}
	}
	array := base("array", "/array", wrappedArray)
	array["response_mode"] = "wrap"
	scalar := base("scalar", "/scalar", wrappedScalar)
	scalar["response_mode"] = "wrap"
	noContent := base("empty", "/empty", empty)
	noContent["allow_empty_success"] = true
	noContent["response_schemas"] = JSON{"204": empty}
	empty200 := base("empty200", "/empty-200", empty)
	empty200["allow_empty_success"] = true
	legacyArray := base("legacyArray", "/array", wrappedArray)
	business := base("business", "/business-failed", object)
	business["response_path"] = []any{"record"}
	business["business_success"] = JSON{"path": []any{"ok"}, "value": true, "error_code": "business_rejected"}
	missing := base("missing", "/business-missing", object)
	missing["response_path"] = []any{"record"}
	missing["business_success"] = business["business_success"]
	success := base("success", "/business-ok", object)
	success["response_path"] = []any{"record"}
	success["business_success"] = business["business_success"]
	legacyObject := base("legacyObject", "/object", object)
	manifest := JSON{"schema": "agenstra.rest-pack.v2", "name": "responses", "version": "1", "guidance": "Test responses", "base_url_env": "API_URL", "capabilities": []any{array, scalar, noContent, empty200, legacyArray, business, missing, success, legacyObject}}
	pack, err := LoadRestPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(pack.Close)
	for _, tc := range []struct {
		name string
		want JSON
		code string
	}{
		{"array", JSON{"result": []any{"A", "B"}}, ""},
		{"scalar", JSON{"result": json.Number("17")}, ""},
		{"empty", JSON{}, ""},
		{"empty200", nil, "upstream_response_invalid"},
		{"legacyArray", nil, "upstream_response_invalid"},
		{"business", nil, "business_rejected"},
		{"missing", nil, "upstream_response_invalid"},
		{"success", JSON{"id": "R1"}, ""},
		{"legacyObject", JSON{"id": "R1"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := pack.Invoke(context.Background(), tc.name, JSON{}, nil)
			if err != nil || result.ErrorCode != tc.code || !reflect.DeepEqual(result.Data, tc.want) {
				t.Fatalf("result = %+v, err = %v; want data %v, code %q", result, err, tc.want, tc.code)
			}
		})
	}
}

func TestRESTRejectsUnsafeResponseMapping(t *testing.T) {
	input := JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}
	output := JSON{"type": "object", "properties": JSON{"result": JSON{"type": "array"}}, "required": []any{"result"}}
	endpoint := RestEndpoint{Name: "records.read", Description: "Read", Method: "GET", Path: "/records", Effect: "read", InputSchema: input, OutputSchema: output}
	for _, check := range []RestBusinessCheck{
		{Path: nil, Value: true, ErrorCode: "business_rejected"},
		{Path: []any{"ok"}, Value: true, ErrorCode: "provider_outcome_unknown"},
		{Path: []any{"ok"}, Value: true, ErrorCode: "unauthorized"},
		{Path: []any{json.Number("-1")}, Value: true, ErrorCode: "business_rejected"},
		{Path: []any{"ok"}, Value: JSON{"nested": true}, ErrorCode: "business_rejected"},
	} {
		endpoint.BusinessSuccess = &check
		if err := validateRestEndpoint(endpoint, nil); err == nil {
			t.Fatalf("accepted unsafe business check: %+v", check)
		}
	}
	endpoint.BusinessSuccess = nil
	endpoint.ResponseMode = "wrap"
	endpoint.OutputSchema = JSON{"type": "object"}
	if err := validateRestEndpoint(endpoint, nil); err == nil {
		t.Fatal("accepted wrap mode without result contract")
	}
	endpoint.ResponseMode = "unknown"
	if err := validateRestEndpoint(endpoint, nil); err == nil {
		t.Fatal("accepted unknown response mode")
	}
}
