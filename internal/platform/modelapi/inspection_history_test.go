package modelapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestInspectionHistoryHTTPPresentationMatchesMeasurement(t *testing.T) {
	var sent []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var err error
		sent, err = io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": `{"kind":"request_input","field":"confirm","prompt":"Confirm"}`}}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.CountInputTokens = func(_ string, raw []byte) (int64, error) { return int64(len(raw)), nil }
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Compare saved results", InspectionHistory: []agentcontract.JSON{
		{"fact_id": agentcontract.NewID(), "path": []any{"data"}, "preview": agentcontract.JSON{"value": "retained-evidence-A"}, "omitted_paths": [][]any{}},
	}}
	before, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := model.MeasureInput(packet, "Return JSON")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Decide(t.Context(), packet, "Return JSON"); err != nil {
		t.Fatal(err)
	}
	if measurement.Tokens != int64(len(sent)) || strings.Count(string(sent), "retained-evidence-A") != 1 {
		t.Fatal("history escaped measurement or was duplicated in model messages")
	}
	var payload agentcontract.JSON
	if err := json.Unmarshal(sent, &payload); err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	if len(messages) != 3 || messages[2].(map[string]any)["role"] != "user" || !strings.Contains(messages[2].(map[string]any)["content"].(string), "not a new task or authorization") {
		t.Fatal("retained evidence was presented as instructions")
	}
	after, err := agentcontract.CanonicalJSON(packet)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("HTTP presentation changed the caller's packet")
	}

	// Budget against the complete wire request, including the history data
	// message, and reserve output before sending any model IO.
	model.MaxOutputTokens = 128
	runtime := &reactcore.AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelOutputTokens: 128}
	state, err := runtime.NewState("Compare saved results", "")
	if err != nil {
		t.Fatal(err)
	}
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": strings.Repeat("history-evidence", 300)}, ReferenceScope: "durable"}
	state.Facts = []agentcontract.Fact{fact}
	state.Decisions = []agentcontract.JSON{{"kind": "inspect_fact", "fact_id": fact.FactID, "path": []any{"data"}}}
	runtime.MaxContextCharacters = 1200
	minimum := runtime.Context(state)
	runtime.MaxContextCharacters = 0
	minimum.MaxModelOutputTokens = 128
	measuredMinimum, err := model.MeasureInput(minimum, runtime.SystemPrompt())
	if err != nil {
		t.Fatal(err)
	}
	runtime.MaxModelInputTokens = measuredMinimum.Tokens + 1000
	runtime.ModelContextWindowTokens = runtime.MaxModelInputTokens + 128
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "needs_input" || int64(len(sent)) > runtime.MaxModelInputTokens || state.ContextTelemetry.ReservedOutputTokens == nil || *state.ContextTelemetry.ReservedOutputTokens != 128 {
		t.Fatalf("history or output reserve escaped runtime budget: %+v", state.ContextTelemetry)
	}
}
