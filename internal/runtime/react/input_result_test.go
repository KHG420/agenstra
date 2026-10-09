package react

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestFinalResultRefsResolveOnlyCitedFacts(t *testing.T) {
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "orders.get", Value: agentcontract.JSON{"data": agentcontract.JSON{"id": "ORDER-42"}}, ReferenceScope: "durable"}
	decision := agentcontract.Decision{Kind: "final", AnswerMarkdown: "Done", FactIDs: []string{fact.FactID}, ResultRefs: []agentcontract.ResultRefRequest{{FactID: fact.FactID, Path: []any{"data", "id"}, EntityType: "order", Label: "Order"}}}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
		return decision, nil
	})}
	state, callErr := r.NewState("Find order", "")
	if callErr != nil {
		t.Error(callErr)
	}
	state.Facts = []agentcontract.Fact{fact}
	if err := r.Step(t.Context(), state, nil); err != nil || state.Status != "completed" || len(state.ResultRefs) != 1 || state.ResultRefs[0].ID != "ORDER-42" || r.Result(state).ResultRefs[0].ID != "ORDER-42" {
		t.Fatal(state.Status, state.ResultRefs, err)
	}
	decision.FactIDs = nil
	var callErr2 error
	state, callErr2 = r.NewState("Find order", "")
	if callErr2 != nil {
		t.Error(callErr2)
	}
	state.Facts = []agentcontract.Fact{fact}
	if err := r.Step(t.Context(), state, nil); err != nil || state.Status == "completed" {
		t.Fatal("uncited result ref accepted", state.Status, err)
	}
	decision.FactIDs = []string{fact.FactID}
	fact.ModelOutput = &agentcontract.ModelOutput{Paths: [][]string{{"status"}}}
	var callErr3 error
	state, callErr3 = r.NewState("Find order", "")
	if callErr3 != nil {
		t.Error(callErr3)
	}
	state.Facts = []agentcontract.Fact{fact}
	if err := r.Step(t.Context(), state, nil); err != nil || state.Status == "completed" {
		t.Fatal("hidden business ID accepted", state.Status, err)
	}
	if _, err := agentcontract.StrictDecision([]byte(`{"kind":"final","answer_markdown":"Done","result_refs":[{"fact_id":"` + fact.FactID + `","path":["data","id"],"id":"FAKE"}]}`)); err == nil {
		t.Fatal("model supplied business ID accepted")
	}
	oversize := agentcontract.RequestedInputSchema{Type: "enum", Enum: []string{strings.Repeat("x", 201)}}
	if oversize.Validate() == nil {
		t.Fatal("oversized enum item accepted")
	}
	var decoded agentcontract.Decision
	if err := json.Unmarshal([]byte(`{"kind":"request_input","field":"x","prompt":"x","input_schema":{"type":"object"}}`), &decoded); err != nil || decoded.Validate() == nil {
		t.Fatal("unsupported schema accepted", err)
	}
}

func TestFinalReferenceFeedbackAllowsCorrectionWithoutExtraBusinessCalls(t *testing.T) {
	for _, kind := range []string{"citations", "path"} {
		t.Run(kind, func(t *testing.T) {
			fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "ui.open", ReferenceScope: "durable", Value: agentcontract.JSON{"data": agentcontract.JSON{"status": "succeeded", "result": agentcontract.JSON{"id": "OBJECT-1"}}}}
			rounds := 0
			model := decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				rounds++
				final := agentcontract.Decision{Kind: "final", AnswerMarkdown: "Opened", FactIDs: []string{fact.FactID}, ResultRefs: []agentcontract.ResultRefRequest{{FactID: fact.FactID, Path: []any{"data", "result", "id"}}}}
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
					raw, callErr4 := json.Marshal(packet.Observations)
					if callErr4 != nil {
						t.Error(callErr4)
					}
					if !strings.Contains(string(raw), "feedback") {
						t.Fatal("missing correction guidance", string(raw))
					}
				}
				return final, nil
			})
			r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model}
			state, callErr5 := r.NewState("Open object", "")
			if callErr5 != nil {
				t.Error(callErr5)
			}
			state.Facts = []agentcontract.Fact{fact}
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
