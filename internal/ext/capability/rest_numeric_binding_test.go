package capability

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRESTIntegerQueryAcceptsEquivalentJSONNumbers(t *testing.T) {
	for _, literal := range []string{"2", "2.0", "2e0"} {
		t.Run(literal, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				value := request.URL.Query().Get("id")
				id, err := strconv.ParseInt(value, 10, 64)
				if err != nil || id != 2 {
					http.Error(w, "integer query syntax rejected", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{"id":2}`)); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			manifest := agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "integer-query-probe",
				"version": "1.0.0", "guidance": "Read one numeric record", "base_url_env": "URL",
				"capabilities": []any{agentcontract.JSON{"name": "records.get", "description": "Read", "method": "GET",
					"path": "/records", "effect": "read", "response_path": []any{},
					"input_schema": agentcontract.JSON{"type": "object", "additionalProperties": false,
						"properties": agentcontract.JSON{"query": agentcontract.JSON{"type": "object", "additionalProperties": false,
							"properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "integer", "minimum": 1}},
							"required":   []any{"id"}}}, "required": []any{"query"}},
					"output_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "integer"}},
						"required": []any{"id"}, "additionalProperties": false}}}}
			pack, err := LoadRestPack(writeTestManifest(t, manifest), map[string]string{"URL": server.URL}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			result, err := pack.Invoke(context.Background(), "records.get",
				agentcontract.JSON{"query": agentcontract.JSON{"id": json.Number(literal)}}, nil)
			if err != nil || result.ErrorCode != "" {
				t.Fatalf("valid JSON Schema integer %s failed native integer query: code=%s err=%v", literal, result.ErrorCode, err)
			}
		})
	}
}

func TestRESTNumericScalarSpellingPreservesExactValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"decimal integer", json.Number("2.0"), "2"},
		{"exponent integer", json.Number("2e0"), "2"},
		{"scaled decimal integer", json.Number("2.50e1"), "25"},
		{"leading fractional zeros", json.Number("0.0200e2"), "2"},
		{"negative integer", json.Number("-2.0"), "-2"},
		{"negative zero", json.Number("-0.0"), "0"},
		{"zero huge exponent", json.Number("0e999999999999999999999999"), "0"},
		{"exact large ID", json.Number("9223372036854775807.0"), "9223372036854775807"},
		{"large exponent", json.Number("1e308"), "1" + strings.Repeat("0", 308)},
		{"fraction", json.Number("1.25"), "1.25"},
		{"fraction near integer", json.Number("2.0000000000000000000000001"), "2.0000000000000000000000001"},
		{"tiny fraction", json.Number("1e-1000000"), "1e-1000000"},
		{"floating integer", float64(1e6), "1000000"},
		{"opaque decimal string", "2.0", "2.0"},
		{"opaque exponent string", "2e0", "2e0"},
		{"boolean", false, "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := restScalarValue(tc.value)
			if err != nil || got != tc.want {
				t.Fatalf("numeric parameter changed value: got %q want %q err=%v", got, tc.want, err)
			}
		})
	}
	for _, value := range []any{math.NaN(), math.Inf(1), json.Number("1e309"), json.Number("true"), json.Number("01")} {
		if _, err := restScalarValue(value); err == nil {
			t.Fatalf("invalid/nonfinite numeric parameter accepted: %v", value)
		}
	}
}

func TestRESTNumericPathHeaderAndRepeatedQueryKeepSchemaAndCallerValues(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.EscapedPath() != "/records/2" || request.Header.Get("X-Record") != "2" ||
			!reflect.DeepEqual(request.URL.Query()["id"], []string{"2", "3"}) ||
			!reflect.DeepEqual(request.URL.Query()["label"], []string{"2.0", "2e0"}) {
			t.Error("numeric request representation or opaque strings changed")
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"id":2}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	object := func(properties agentcontract.JSON, required ...any) agentcontract.JSON {
		return agentcontract.JSON{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	integer := agentcontract.JSON{"type": "integer", "minimum": 1}
	endpoint := agentcontract.JSON{"name": "records.get", "description": "Read", "method": "GET", "path": "/records/{id}", "effect": "read",
		"input_schema": object(agentcontract.JSON{
			"path":    object(agentcontract.JSON{"id": integer}, "id"),
			"headers": object(agentcontract.JSON{"X-Record": integer}, "X-Record"),
			"query": object(agentcontract.JSON{"id": agentcontract.JSON{"type": "array", "items": integer},
				"label": agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "string"}}}, "id", "label"),
		}, "path", "headers", "query"),
		"output_schema": object(agentcontract.JSON{"id": integer}, "id"), "response_path": []any{}}
	manifest := agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "numeric-binding", "version": "1.0.0",
		"guidance": "Read numeric records", "base_url_env": "URL", "capabilities": []any{endpoint}}
	pack, err := LoadRestPack(writeTestManifest(t, manifest), map[string]string{"URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	args := agentcontract.JSON{"path": agentcontract.JSON{"id": json.Number("2e0")},
		"headers": agentcontract.JSON{"X-Record": json.Number("2.0")},
		"query":   agentcontract.JSON{"id": []any{json.Number("2.0"), json.Number("3e0")}, "label": []any{"2.0", "2e0"}}}
	before, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := pack.Invoke(context.Background(), "records.get", args, nil)
	if err != nil || result.ErrorCode != "" || calls != 1 {
		t.Fatal("numeric request failed", result.ErrorCode, calls, err)
	}
	after, err := json.Marshal(args)
	if err != nil || string(before) != string(after) {
		t.Fatal("HTTP formatting mutated caller arguments", err)
	}
	for _, value := range []any{"2.0", true, json.Number("2.1")} {
		bad := agentcontract.JSON{"path": agentcontract.JSON{"id": value}, "headers": args["headers"], "query": args["query"]}
		result, err := pack.Invoke(context.Background(), "records.get", bad, nil)
		if err != nil || result.ErrorCode != "capability_input_invalid" || calls != 1 {
			t.Fatal("integer input contract was weakened", result.ErrorCode, calls, err)
		}
	}
}
