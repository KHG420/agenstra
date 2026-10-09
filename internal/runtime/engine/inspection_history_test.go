package engine

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInspectionHistoryRetainsEarlierEvidenceAfterRestore(t *testing.T) {
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{}}
	a := Fact{FactID: NewID(), Value: JSON{"data": JSON{"id": "A", "count": 4}}, ReferenceScope: "durable"}
	b := Fact{FactID: NewID(), Value: JSON{"data": JSON{"id": "B", "count": 5}}, ReferenceScope: "durable"}
	r := &AgentRuntime{Provider: provider, Model: &coreTestModel{decisions: []Decision{
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: b.FactID, Path: []any{"data"}},
	}}}
	state, err := r.NewState("Compare both saved results", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []Fact{a, b}
	for range 2 {
		if err := r.Step(t.Context(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Older checkpoints contain only the decision journal and last inspection.
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored RuntimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	packet, err := objectOf(r.Context(&restored))
	if err != nil {
		t.Fatal(err)
	}
	history, ok := packet["inspection_history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("earlier inspected evidence vanished after the next inspection: %v", history)
	}
	item := history[0].(map[string]any)
	if item["fact_id"] != a.FactID || item["preview"].(map[string]any)["value"].(map[string]any)["id"] != "A" {
		t.Fatal("history lost original fact identity, path or evidence", item)
	}
	if packet["inspected_fact"].(map[string]any)["fact_id"] != b.FactID || provider.called != 0 {
		t.Fatal("history changed the current inspection or repeated a business read")
	}
}

func TestInspectionHistoryDeduplicatesAndRechecksVisibility(t *testing.T) {
	a := Fact{FactID: NewID(), Value: JSON{"data": JSON{"id": "A", "secret": "private-evidence"}}, ModelOutput: &ModelOutput{Paths: [][]string{{"id"}}}, ReferenceScope: "durable"}
	b := Fact{FactID: NewID(), Value: JSON{"data": JSON{"id": "B"}}, ReferenceScope: "durable"}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: &coreTestModel{decisions: []Decision{
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data", "secret"}},
		{Kind: "inspect_fact", FactID: b.FactID, Path: []any{"data"}},
	}}}
	state, err := r.NewState("Compare saved results", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []Fact{a, b}
	for range 4 {
		if err := r.Step(t.Context(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	before, err := CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	packet := r.Context(state)
	raw, err := CanonicalJSON(packet)
	if err != nil || strings.Contains(string(raw), "private-evidence") || len(packet.InspectionHistory) != 1 {
		t.Fatalf("duplicate or hidden inspection entered context: %s %v", raw, err)
	}
	packet.InspectionHistory[0]["preview"].(map[string]any)["value"].(map[string]any)["id"] = "caller mutation"
	after, err := CanonicalJSON(state)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("history projection changed persisted evidence")
	}
	// Rebuilding a restored view must follow the Fact's current visibility rule.
	state.Facts[0].ModelOutput = &ModelOutput{Paths: [][]string{}}
	packet = r.Context(state)
	raw, err = CanonicalJSON(packet.InspectionHistory)
	if err != nil || strings.Contains(string(raw), `"id":"A"`) {
		t.Fatalf("history bypassed model_output on rebuild: %s %v", raw, err)
	}
}

func TestInspectionHistoryBoundAndArrayMetadata(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}}
	state, err := r.NewState("Compare retained row results", "")
	if err != nil {
		t.Fatal(err)
	}
	for range 15 {
		fact := Fact{FactID: NewID(), Value: JSON{"data": []any{JSON{"id": "first"}, JSON{"id": "second"}}}, ReferenceScope: "durable"}
		state.Facts = append(state.Facts, fact)
		state.Decisions = append(state.Decisions, JSON{"kind": "inspect_fact", "fact_id": fact.FactID, "path": []any{"data", 1}})
	}
	last := state.Facts[14]
	state.InspectedFact = JSON{"fact_id": last.FactID, "path": []any{"data", 1}}
	packet := r.Context(state)
	if len(packet.InspectionHistory) != 12 || packet.InspectionHistory[0]["fact_id"] != state.Facts[2].FactID {
		t.Fatal("history did not retain the most recent distinct paths", packet.InspectionHistory)
	}
	for _, item := range packet.InspectionHistory {
		if item["parent_array_length"] != 2 || item["inspected_index"] != 1 {
			t.Fatal("retained row lost array metadata", item)
		}
	}
	if !strings.Contains(strings.Join(packet.ContextOmissions, "\n"), "inspection_history: 2 older entries") {
		t.Fatal("history bound silently dropped evidence", packet.ContextOmissions)
	}
	// Empty root paths are omitted from Decision JSON and still rebuild correctly.
	state.Decisions = []JSON{{"kind": "inspect_fact", "fact_id": state.Facts[0].FactID}}
	state.InspectedFact = nil
	packet = r.Context(state)
	if len(packet.InspectionHistory) != 1 || packet.InspectionHistory[0]["fact_id"] != state.Facts[0].FactID {
		t.Fatal("root inspection was lost on journal reconstruction")
	}
}

func TestInspectionHistoryBudgetKeepsCurrentInspection(t *testing.T) {
	a := Fact{FactID: NewID(), Value: JSON{"data": JSON{"body": strings.Repeat("a", 4500)}}, ReferenceScope: "durable"}
	b := Fact{FactID: NewID(), Value: JSON{"data": JSON{"id": "B"}}, ReferenceScope: "durable"}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: &coreTestModel{decisions: []Decision{
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: b.FactID, Path: []any{"data"}},
	}}}
	state, err := r.NewState("Compare saved results", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []Fact{a, b}
	for range 2 {
		if err := r.Step(t.Context(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	before, err := CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	r.MaxContextCharacters = contextCharacters(r.systemPrompt()) + 1500
	packet := r.Context(state)
	if contextCharacters(packet)+len(r.systemPrompt()) > r.MaxContextCharacters || len(packet.InspectionHistory) != 0 {
		t.Fatal("older inspections made the bounded context oversized")
	}
	if packet.InspectedFact["fact_id"] != b.FactID || !strings.Contains(strings.Join(packet.ContextOmissions, "\n"), "inspection_history: 1 older entries") {
		t.Fatal("projection lost the required current inspection or omission notice")
	}
	after, err := CanonicalJSON(state)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("budgeting deleted retained execution evidence")
	}
}

func TestInspectionHistoryHTTPPresentationMatchesMeasurement(t *testing.T) {
	var sent []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var err error
		sent, err = io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": `{"kind":"request_input","field":"confirm","prompt":"Confirm"}`}}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.CountInputTokens = func(_ string, raw []byte) (int64, error) { return int64(len(raw)), nil }
	packet := ContextPacket{Schema: "agenstra.context.v1", Instruction: "Compare saved results", InspectionHistory: []JSON{
		{"fact_id": NewID(), "path": []any{"data"}, "preview": JSON{"value": "retained-evidence-A"}, "omitted_paths": [][]any{}},
	}}
	before, err := CanonicalJSON(packet)
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
	var payload JSON
	if err := json.Unmarshal(sent, &payload); err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	if len(messages) != 3 || messages[2].(map[string]any)["role"] != "user" || !strings.Contains(messages[2].(map[string]any)["content"].(string), "not a new task or authorization") {
		t.Fatal("retained evidence was presented as instructions")
	}
	after, err := CanonicalJSON(packet)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("HTTP presentation changed the caller's packet")
	}
	// Budget against the complete wire request, including the history data
	// message, and reserve output before sending any model IO.
	model.MaxOutputTokens = 128
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: model, MaxModelOutputTokens: 128}
	state, err := runtime.NewState("Compare saved results", "")
	if err != nil {
		t.Fatal(err)
	}
	fact := Fact{FactID: NewID(), Value: JSON{"data": strings.Repeat("history-evidence", 300)}, ReferenceScope: "durable"}
	state.Facts = []Fact{fact}
	state.Decisions = []JSON{{"kind": "inspect_fact", "fact_id": fact.FactID, "path": []any{"data"}}}
	candidate := runtime.contextCandidate(state)
	minimum := budgetContext(candidate, state, 1200)
	minimum.MaxModelOutputTokens = 128
	measuredMinimum, err := model.MeasureInput(minimum, runtime.systemPrompt())
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
