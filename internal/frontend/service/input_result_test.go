package service

import (
	"net/http/httptest"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
)

func TestRequestedInputSchemasAndDurableSupply(t *testing.T) {
	for _, schema := range []agentcontract.RequestedInputSchema{
		{Type: "enum", Enum: []string{"yes", "no"}},
		{Type: "date"},
		{Type: "string", MinLength: 2, MaxLength: 4},
	} {
		if err := schema.Validate(); err != nil {
			t.Fatal(schema, err)
		}
	}
	for _, schema := range []agentcontract.RequestedInputSchema{
		{Type: "enum", Enum: []string{"yes", "yes"}},
		{Type: "enum", Enum: make([]string, 51)},
		{Type: "date", MinLength: 1},
		{Type: "object"},
	} {
		if schema.Validate() == nil {
			t.Fatal("accepted unsupported input schema", schema)
		}
	}
	if durablehost.ValidateRequestedInput(&agentcontract.RequestedInputSchema{Type: "date"}, "2026-02-29") == nil || durablehost.ValidateRequestedInput(&agentcontract.RequestedInputSchema{Type: "date"}, "2026-10-02") != nil {
		t.Fatal("date validation")
	}
	choice := &agentcontract.RequestedInputSchema{Type: "enum", Enum: []string{"yes", "no"}}
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{decisions: []agentcontract.Decision{{Kind: "request_input", Field: "confirm", Prompt: "Confirm?", InputSchema: choice}}})
	run, err := h.Create(t.Context(), "alice", "records", "Ask for confirmation", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_input" {
		t.Fatal(run.Status, err)
	}
	if _, err := h.SupplyInput(t.Context(), run.RunID, "alice", "confirm", "maybe", run.Revision); agentcontract.ErrorCode(err) != "input_invalid" {
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
