package agenstra_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

type catalogNumberProvider struct {
	nativeInventoryProvider
	schema agenstra.JSON
}

func (p *catalogNumberProvider) Capabilities() map[string]agenstra.CapabilityDescription {
	capabilities := p.nativeInventoryProvider.Capabilities()
	capability := capabilities["inventory.set"]
	capability.InputSchema = p.schema
	capabilities["inventory.set"] = capability
	return capabilities
}

func TestSDKRuntimePreservesCatalogNumbersOnNativeWire(t *testing.T) {
	decode := func(raw []byte) (agenstra.JSON, error) {
		var value agenstra.JSON
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		err := decoder.Decode(&value)
		return value, err
	}
	schema, err := decode([]byte(`{"type":"object","properties":{"id":{"type":"integer","minimum":9007199254740993,"maximum":9223372036854775807},"amount":{"type":"number","const":0.12345678901234567890123456789}},"required":["id"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	want, err := agenstra.CanonicalJSON(schema)
	if err != nil {
		t.Fatal(err)
	}
	provider := &catalogNumberProvider{schema: schema}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		payload, err := decode(raw)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		functionName := nativeSDKFunction(t, payload, "inventory.set")
		for _, tool := range payload["tools"].([]any) {
			function := tool.(map[string]any)["function"].(map[string]any)
			if function["name"] == functionName {
				got, err := agenstra.CanonicalJSON(function["parameters"])
				if err != nil || string(got) != string(want) {
					t.Errorf("runtime/model boundary rounded the original numeric schema: got %s; want %s; error %v", got, want, err)
				}
			}
		}
		nativeSDKReply(t, w, "final", agenstra.JSON{"answer_markdown": "No business operation requested.", "fact_ids": []string{}})
	}))
	defer server.Close()
	model, err := agenstra.NewHTTPJSONDecisionModel("fixture", server.URL, "fixture", 5*time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.DecisionOutputMode = "native_tools"
	runtime := &agenstra.AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"inventory.list": true, "inventory.set": true}}
	state, err := runtime.NewState("Answer without invoking any business operation.", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || len(provider.calls) != 0 || state.Status != "completed" {
		t.Fatal("schema presentation test executed business work or failed to complete", requests.Load(), provider.calls, state.Status)
	}
}
