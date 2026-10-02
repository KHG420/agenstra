package agenstra

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestedInputSchemasAndDurableSupply(t *testing.T) {
	for _, schema := range []RequestedInputSchema{
		{Type: "enum", Enum: []string{"yes", "no"}},
		{Type: "date"},
		{Type: "string", MinLength: 2, MaxLength: 4},
	} {
		if err := schema.Validate(); err != nil {
			t.Fatal(schema, err)
		}
	}
	for _, schema := range []RequestedInputSchema{
		{Type: "enum", Enum: []string{"yes", "yes"}},
		{Type: "enum", Enum: make([]string, 51)},
		{Type: "date", MinLength: 1},
		{Type: "object"},
	} {
		if schema.Validate() == nil {
			t.Fatal("accepted unsupported input schema", schema)
		}
	}
	if ValidateRequestedInput(&RequestedInputSchema{Type: "date"}, "2026-02-29") == nil || ValidateRequestedInput(&RequestedInputSchema{Type: "date"}, "2026-10-02") != nil {
		t.Fatal("date validation")
	}
	choice := &RequestedInputSchema{Type: "enum", Enum: []string{"yes", "no"}}
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{decisions: []Decision{{Kind: "request_input", Field: "confirm", Prompt: "Confirm?", InputSchema: choice}}})
	run, err := h.Create(t.Context(), "alice", "records", "Ask for confirmation", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_input" {
		t.Fatal(run.Status, err)
	}
	if _, err := h.SupplyInput(t.Context(), run.RunID, "alice", "confirm", "maybe", run.Revision); ErrorCode(err) != "input_invalid" {
		t.Fatal("invalid enum answer accepted", err)
	} else {
		response := httptest.NewRecorder()
		serverError(response, err)
		if response.Code != 422 {
			t.Fatal("input validation HTTP status", response.Code)
		}
	}
	current, err := h.Get(t.Context(), run.RunID, "alice")
	if err != nil || current.Revision != run.Revision || current.Status != "needs_input" {
		t.Fatal("invalid input changed run", current.Status, err)
	}
	current, err = h.SupplyInput(t.Context(), run.RunID, "alice", "confirm", "yes", run.Revision)
	if err != nil || current.Status != "queued" {
		t.Fatal(current.Status, err)
	}
	state := current.State["runtime"].(map[string]any)
	if state["input_schema"] != nil {
		t.Fatal("input schema remained active")
	}
}

func TestFinalResultRefsResolveOnlyCitedFacts(t *testing.T) {
	fact := Fact{FactID: NewID(), SourceCapability: "orders.get", Value: JSON{"data": JSON{"id": "ORDER-42"}}, ReferenceScope: "durable"}
	decision := Decision{Kind: "final", AnswerMarkdown: "Done", FactIDs: []string{fact.FactID}, ResultRefs: []ResultRefRequest{{FactID: fact.FactID, Path: []any{"data", "id"}, EntityType: "order", Label: "Order"}}}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: decisionModelFunc(func(context.Context, ContextPacket, string) (Decision, error) { return decision, nil })}
	state, _ := r.NewState("Find order", "")
	state.Facts = []Fact{fact}
	if err := r.Step(t.Context(), state, nil); err != nil || state.Status != "completed" || len(state.ResultRefs) != 1 || state.ResultRefs[0].ID != "ORDER-42" || r.Result(state).ResultRefs[0].ID != "ORDER-42" {
		t.Fatal(state.Status, state.ResultRefs, err)
	}
	decision.FactIDs = nil
	state, _ = r.NewState("Find order", "")
	state.Facts = []Fact{fact}
	if err := r.Step(t.Context(), state, nil); err != nil || state.Status == "completed" {
		t.Fatal("uncited result ref accepted", state.Status, err)
	}
	decision.FactIDs = []string{fact.FactID}
	fact.ModelOutput = &ModelOutput{Paths: [][]string{{"status"}}}
	state, _ = r.NewState("Find order", "")
	state.Facts = []Fact{fact}
	if err := r.Step(t.Context(), state, nil); err != nil || state.Status == "completed" {
		t.Fatal("hidden business ID accepted", state.Status, err)
	}
	if _, err := strictDecision([]byte(`{"kind":"final","answer_markdown":"Done","result_refs":[{"fact_id":"` + fact.FactID + `","path":["data","id"],"id":"FAKE"}]}`)); err == nil {
		t.Fatal("model supplied business ID accepted")
	}
	oversize := RequestedInputSchema{Type: "enum", Enum: []string{strings.Repeat("x", 201)}}
	if oversize.Validate() == nil {
		t.Fatal("oversized enum item accepted")
	}
	var decoded Decision
	if err := json.Unmarshal([]byte(`{"kind":"request_input","field":"x","prompt":"x","input_schema":{"type":"object"}}`), &decoded); err != nil || decoded.Validate() == nil {
		t.Fatal("unsupported schema accepted", err)
	}
}
