package react

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestInspectionHistoryRetainsEarlierEvidenceAfterRestore(t *testing.T) {
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
	a := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"id": "A", "count": 4}}, ReferenceScope: "durable"}
	b := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"id": "B", "count": 5}}, ReferenceScope: "durable"}
	r := &AgentRuntime{Provider: provider, Model: &coreTestModel{decisions: []agentcontract.Decision{
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: b.FactID, Path: []any{"data"}},
	}}}
	state, err := r.NewState("Compare both saved results", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []agentcontract.Fact{a, b}
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
	var restored agentcontract.RuntimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	packet, err := agentcontract.ObjectOf(r.Context(&restored))
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
	a := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"id": "A", "secret": "private-evidence"}}, ModelOutput: &agentcontract.ModelOutput{Paths: [][]string{{"id"}}}, ReferenceScope: "durable"}
	b := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"id": "B"}}, ReferenceScope: "durable"}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: &coreTestModel{decisions: []agentcontract.Decision{
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data", "secret"}},
		{Kind: "inspect_fact", FactID: b.FactID, Path: []any{"data"}},
	}}}
	state, err := r.NewState("Compare saved results", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []agentcontract.Fact{a, b}
	for range 4 {
		if err := r.Step(t.Context(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	before, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	packet := r.Context(state)
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil || strings.Contains(string(raw), "private-evidence") || len(packet.InspectionHistory) != 1 {
		t.Fatalf("duplicate or hidden inspection entered context: %s %v", raw, err)
	}
	packet.InspectionHistory[0]["preview"].(map[string]any)["value"].(map[string]any)["id"] = "caller mutation"
	after, err := agentcontract.CanonicalJSON(state)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("history projection changed persisted evidence")
	}

	// Rebuilding a restored view must follow the Fact's current visibility rule.
	state.Facts[0].ModelOutput = &agentcontract.ModelOutput{Paths: [][]string{}}
	packet = r.Context(state)
	raw, err = agentcontract.CanonicalJSON(packet.InspectionHistory)
	if err != nil || strings.Contains(string(raw), `"id":"A"`) {
		t.Fatalf("history bypassed model_output on rebuild: %s %v", raw, err)
	}
}

func TestInspectionHistoryBoundAndArrayMetadata(t *testing.T) {
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}}
	state, err := r.NewState("Compare retained row results", "")
	if err != nil {
		t.Fatal(err)
	}
	for range 15 {
		fact := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": []any{agentcontract.JSON{"id": "first"}, agentcontract.JSON{"id": "second"}}}, ReferenceScope: "durable"}
		state.Facts = append(state.Facts, fact)
		state.Decisions = append(state.Decisions, agentcontract.JSON{"kind": "inspect_fact", "fact_id": fact.FactID, "path": []any{"data", 1}})
	}
	last := state.Facts[14]
	state.InspectedFact = agentcontract.JSON{"fact_id": last.FactID, "path": []any{"data", 1}}
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
	state.Decisions = []agentcontract.JSON{{"kind": "inspect_fact", "fact_id": state.Facts[0].FactID}}
	state.InspectedFact = nil
	packet = r.Context(state)
	if len(packet.InspectionHistory) != 1 || packet.InspectionHistory[0]["fact_id"] != state.Facts[0].FactID {
		t.Fatal("root inspection was lost on journal reconstruction")
	}
}

func TestInspectionHistoryBudgetKeepsCurrentInspection(t *testing.T) {
	a := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"body": strings.Repeat("a", 4500)}}, ReferenceScope: "durable"}
	b := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"id": "B"}}, ReferenceScope: "durable"}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: &coreTestModel{decisions: []agentcontract.Decision{
		{Kind: "inspect_fact", FactID: a.FactID, Path: []any{"data"}},
		{Kind: "inspect_fact", FactID: b.FactID, Path: []any{"data"}},
	}}}
	state, err := r.NewState("Compare saved results", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []agentcontract.Fact{a, b}
	for range 2 {
		if err := r.Step(t.Context(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	before, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	r.MaxContextCharacters = contextCharacters(r.SystemPrompt()) + 1500
	packet := r.Context(state)
	if contextCharacters(packet)+len(r.SystemPrompt()) > r.MaxContextCharacters || len(packet.InspectionHistory) != 0 {
		t.Fatal("older inspections made the bounded context oversized")
	}
	if packet.InspectedFact["fact_id"] != b.FactID || !strings.Contains(strings.Join(packet.ContextOmissions, "\n"), "inspection_history: 1 older entries") {
		t.Fatal("projection lost the required current inspection or omission notice")
	}
	after, err := agentcontract.CanonicalJSON(state)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("budgeting deleted retained execution evidence")
	}
}
