package capability

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func restNumericBodyPack(t *testing.T, server *httptest.Server, bodySchema agentcontract.JSON) *RestPack {
	t.Helper()
	manifest := agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "numeric-body", "version": "1.0.0",
		"guidance": "Send structured numeric values", "base_url_env": "URL", "capabilities": []any{agentcontract.JSON{
			"name": "records.batch", "description": "Submit numeric values", "method": "POST", "path": "/records",
			"effect": "write", "idempotency_header": "Idempotency-Key", "response_path": []any{},
			"input_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"body": bodySchema},
				"required": []any{"body"}, "additionalProperties": false},
			"output_schema": agentcontract.JSON{"type": "object", "additionalProperties": false},
		}}}
	pack, err := LoadRestPack(writeTestManifest(t, manifest), map[string]string{"URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func TestRESTJSONBodyBindsEquivalentIntegerNumbers(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		bodies = append(bodies, string(raw))
		var body struct {
			IDs []int64 `json:"ids"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || !reflect.DeepEqual(body.IDs, []int64{2, 25, math.MaxInt64}) {
			http.Error(w, "integer body syntax rejected", http.StatusBadRequest)
			return
		}
		if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Idempotency-Key") != "same-operation" {
			t.Error("request headers changed")
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	pack := restNumericBodyPack(t, server, agentcontract.JSON{"type": "object",
		"properties": agentcontract.JSON{"ids": agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "integer", "minimum": 1}}},
		"required":   []any{"ids"}, "additionalProperties": false})
	for _, ids := range [][]any{
		{json.Number("2.0"), json.Number("2.50e1"), json.Number("9223372036854775807.0")},
		{json.Number("2e0"), float64(25), int64(math.MaxInt64)},
	} {
		args := agentcontract.JSON{"body": agentcontract.JSON{"ids": ids}}
		before, err := agentcontract.CanonicalJSON(args)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			result, err := pack.Invoke(context.Background(), "records.batch", args, &agentcontract.InvocationContext{IdempotencyKey: "same-operation"})
			if err != nil || result.ErrorCode != "" {
				t.Fatalf("valid integer JSON body rejected: code=%s err=%v", result.ErrorCode, err)
			}
		}
		after, err := agentcontract.CanonicalJSON(args)
		if err != nil || string(before) != string(after) {
			t.Fatal("HTTP encoding changed caller arguments or their canonical hash input", err)
		}
	}
	for _, body := range bodies {
		if body != `{"ids":[2,25,9223372036854775807]}` {
			t.Fatalf("integer value or repeated request bytes changed: %s", body)
		}
	}
	for _, value := range []any{"2.0", true, json.Number("2.1")} {
		result, err := pack.Invoke(context.Background(), "records.batch", agentcontract.JSON{"body": agentcontract.JSON{"ids": []any{value}}},
			&agentcontract.InvocationContext{IdempotencyKey: "same-operation"})
		if err != nil || result.ErrorCode != "capability_input_invalid" || len(bodies) != 4 {
			t.Fatal("integer body contract was weakened", result.ErrorCode, len(bodies), err)
		}
	}
}

func TestRESTJSONBodyPreservesExactFractionsAndOtherValues(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		received = string(raw)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	pack := restNumericBodyPack(t, server, agentcontract.JSON{"type": "object"})
	for _, tc := range []struct {
		name string
		body any
		want string
	}{
		{"JSON values", agentcontract.JSON{
			"exact_fraction": json.Number("2.0000000000000000000000001"), "tiny": json.Number("1e-1000000"),
			"large_integer": json.Number("1" + strings.Repeat("0", 400)), "zero": json.Number("-0.0"),
			"items": []any{json.Number("2e0"), "2.0", "2e0", false, nil, json.Number("1.25")},
		}, `{"exact_fraction":2.0000000000000000000000001,"items":[2,"2.0","2e0",false,null,1.25],"large_integer":1` + strings.Repeat("0", 400) + `,"tiny":1e-1000000,"zero":0}`},
		{"typed Go body", &struct {
			IDs   []int64                `json:"ids"`
			Extra map[string]json.Number `json:"extra"`
		}{[]int64{2, math.MaxInt64}, map[string]json.Number{"count": "2e0", "fraction": "1.25"}},
			`{"extra":{"count":2,"fraction":1.25},"ids":[2,9223372036854775807]}`},
		{"raw JSON body", json.RawMessage(`{"nested":{"id":2.0},"fraction":1.00000000000000000001}`),
			`{"fraction":1.00000000000000000001,"nested":{"id":2}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			result, err := pack.Invoke(context.Background(), "records.batch", agentcontract.JSON{"body": tc.body},
				&agentcontract.InvocationContext{IdempotencyKey: "same-operation"})
			if err != nil || result.ErrorCode != "" || received != tc.want {
				t.Fatalf("JSON body value changed: code=%s err=%v\ngot  %s\nwant %s", result.ErrorCode, err, received, tc.want)
			}
			after, err := json.Marshal(tc.body)
			if err != nil || string(before) != string(after) {
				t.Fatal("JSON body encoding mutated caller values", err)
			}
		})
	}
}

func TestLegacyRESTJSONBodyBindsIntegerNumbers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			ID int64 `json:"id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.ID != 2 {
			http.Error(w, "integer body syntax rejected", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"id":2}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	manifest := agentcontract.JSON{"schema": "agenstra.capability-pack.v1", "name": "legacy-body", "guidance": "Read numeric records",
		"capabilities": []any{agentcontract.JSON{"name": "records.get", "version": "1", "description": "Read", "url_env": "URL",
			"method": "POST", "inputs": agentcontract.JSON{"id": agentcontract.JSON{"type": "integer", "description": "ID"}}}}}
	pack, err := LoadLegacyPack(writeTestManifest(t, manifest), map[string]string{"URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{json.Number("2.0"), json.Number("2e0"), float64(2)} {
		result, err := pack.Invoke(context.Background(), "records.get", agentcontract.JSON{"id": value}, nil)
		if err != nil || result.ErrorCode != "" {
			t.Fatalf("legacy valid integer JSON body rejected: code=%s err=%v", result.ErrorCode, err)
		}
	}
}
