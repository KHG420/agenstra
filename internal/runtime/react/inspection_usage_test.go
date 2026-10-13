package react

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestInspectionFeedbackTargetsMissingOriginalPathsAfterRestore(t *testing.T) {
	entries := agentcontract.JSON{}
	for i := range 80 {
		entries[fmt.Sprintf("item-%02d", i)] = strings.Repeat("private-evidence-", 10)
	}
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"entries": entries}}, ReferenceScope: "durable"}
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
	r := &AgentRuntime{Provider: provider, Model: &coreTestModel{decisions: []agentcontract.Decision{{Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "entries"}}}}}
	state, err := r.NewState("Return all saved entries", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []agentcontract.Fact{fact}
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	omissions := state.InspectedFact["omitted_paths"].([][]any)
	if len(omissions) <= 4 {
		t.Fatal("fixture no longer exercises bounded inspection or omission hints")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored agentcontract.RuntimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	before, err := agentcontract.CanonicalJSON(restored)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []*agentcontract.RuntimeState{state, &restored} {
		prompt := inspectionUsagePrompt(r.Context(current).InspectedFact)
		if !strings.Contains(prompt, "This inspected view is incomplete") || strings.Contains(prompt, "has no omitted_paths") || strings.Contains(prompt, "private-evidence-") {
			t.Fatal("partial view was called complete or raw business text entered feedback", prompt)
		}
		for i, path := range omissions[:5] {
			if len(path) < 2 || path[0] != "value" {
				t.Fatal("fixture no longer exercises a specific omitted child", path)
			}
			original := append([]any{"data", "entries"}, path[1:]...)
			encoded, err := agentcontract.CanonicalJSON(original)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(prompt, string(encoded)) != (i < 4) {
				t.Fatal("missing-path hints lost their original Fact path or four-path bound", prompt)
			}
		}
	}
	after, err := agentcontract.CanonicalJSON(restored)
	if err != nil || string(after) != string(before) {
		t.Fatal("feedback changed restored evidence", err)
	}
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		if !strings.Contains(prompt, "This inspected view is incomplete") {
			t.Fatal("bounded inspection guidance did not reach the decision model")
		}
		path := append([]any{"data", "entries"}, omissions[0][1:]...)
		return agentcontract.Decision{Kind: "inspect_fact", FactID: packet.InspectedFact["fact_id"].(string), Path: path}, nil
	})
	if err := r.Step(t.Context(), &restored, nil); err != nil {
		t.Fatal(err)
	}
	if len(restored.InspectedFact["omitted_paths"].([][]any)) != 0 || provider.called != 0 {
		t.Fatal("specific child inspection did not recover saved evidence without business replay")
	}
}

func TestInspectionFeedbackKeepsNullAndErrorViewsDistinct(t *testing.T) {
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": nil}, ReferenceScope: "durable"}
	view, err := inspectionView(map[string]agentcontract.Fact{fact.FactID: fact}, fact.FactID, []any{"data"})
	if err != nil {
		t.Fatal(err)
	}
	if prompt := inspectionUsagePrompt(view); !strings.Contains(prompt, "has no omitted_paths") {
		t.Fatal("a visible business null was treated as missing evidence", prompt)
	}
	if prompt := inspectionUsagePrompt(agentcontract.JSON{"error_code": "fact_reference_path_invalid"}); prompt != "" {
		t.Fatal("an unsuccessful inspection was described as complete", prompt)
	}
}
