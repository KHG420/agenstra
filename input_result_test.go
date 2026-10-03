package agenstra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestedInputPromptRecoversObservedSchemaMismatch(t *testing.T) {
	// The real model used standard JSON Schema's string+enum form twice. On
	// recovery it must have an executable example of this protocol's enum form.
	invalid := `{"kind":"request_input","field":"choice","prompt":"Which option?","input_schema":{"type":"string","enum":["first","second"]}}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		attempt := requests.Add(1)
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || len(payload.Messages) == 0 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		response := invalid
		if attempt > 1 {
			for _, line := range strings.Split(payload.Messages[0].Content, "\n") {
				if example, ok := strings.CutPrefix(line, "Input enum schema: "); ok {
					var corrected JSON
					if err := json.Unmarshal([]byte(invalid), &corrected); err != nil {
						t.Error(err)
						return
					}
					corrected["input_schema"] = json.RawMessage(example)
					raw, err := json.Marshal(corrected)
					if err != nil {
						t.Error(err)
						return
					}
					response = string(raw)
				}
			}
		}
		if err := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": response}}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: model}
	state, err := r.NewState("Ask for a missing choice", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "needs_input" || requests.Load() != 2 || state.InputSchema == nil || state.InputSchema.Type != "enum" {
		t.Fatalf("schema recovery failed: status=%s requests=%d schema=%+v", state.Status, requests.Load(), state.InputSchema)
	}
	if state.ModelUsage.InvalidResponses != 1 || state.ModelUsage.FormatRecoveryRequests != 1 {
		t.Fatalf("recovery usage missing: %+v", state.ModelUsage)
	}
	if ValidateRequestedInput(state.InputSchema, state.InputSchema.Enum[0]) != nil || ValidateRequestedInput(state.InputSchema, "invalid") == nil {
		t.Fatal("prompted enum did not retain strict input validation")
	}
}

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

func TestFinalReferenceFeedbackAllowsCorrectionWithoutExtraBusinessCalls(t *testing.T) {
	for _, kind := range []string{"citations", "path"} {
		t.Run(kind, func(t *testing.T) {
			fact := Fact{FactID: NewID(), SourceCapability: "ui.open", ReferenceScope: "durable", Value: JSON{"data": JSON{"status": "succeeded", "result": JSON{"id": "OBJECT-1"}}}}
			rounds := 0
			model := decisionModelFunc(func(_ context.Context, packet ContextPacket, _ string) (Decision, error) {
				rounds++
				final := Decision{Kind: "final", AnswerMarkdown: "Opened", FactIDs: []string{fact.FactID}, ResultRefs: []ResultRefRequest{{FactID: fact.FactID, Path: []any{"data", "result", "id"}}}}
				if rounds == 1 {
					if kind == "citations" {
						final.FactIDs = nil
					} else {
						final.ResultRefs[0].Path = []any{"data", "id"}
					}
				} else {
					if len(packet.Observations) == 0 {
						t.Fatal("missing rejection observation")
					}
					raw, _ := json.Marshal(packet.Observations)
					if !strings.Contains(string(raw), "feedback") {
						t.Fatal("missing correction guidance", string(raw))
					}
				}
				return final, nil
			})
			r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: model}
			state, _ := r.NewState("Open object", "")
			state.Facts = []Fact{fact}
			if err := r.Step(t.Context(), state, nil); err != nil || state.Status == "completed" {
				t.Fatal("invalid answer accepted", err)
			}
			if err := r.Step(t.Context(), state, nil); err != nil || state.Status != "completed" || state.ResultRefs[0].ID != "OBJECT-1" {
				t.Fatal("correction failed", state.Status, err)
			}
			if rounds != 2 {
				t.Fatal(rounds)
			}
		})
	}
}
