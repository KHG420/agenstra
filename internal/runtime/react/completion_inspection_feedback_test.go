package react

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletionFeedbackInspectsSavedEvidenceWithoutReplayingWrite(t *testing.T) {
	r, state := consistencyFixture(t)
	entries := agentcontract.JSON{}
	ids := []any{}
	for i := range 80 {
		key := fmt.Sprintf("item-%02d", i)
		entries[key] = strings.Repeat("e", 40)
		ids = append(ids, strings.Repeat("i", 40))
	}
	state.Facts[0].Value = agentcontract.JSON{"data": agentcontract.JSON{"ids": ids, "entries": entries}}
	factID := state.Facts[0].FactID
	calls := 0
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		calls++
		if len(packet.Facts) != 1 || len(packet.Facts[0].OmittedPaths) == 0 {
			t.Fatal("fixture no longer exercises an incomplete Fact preview", packet.Facts)
		}
		data, ok := packet.Facts[0].Value["data"].(map[string]any)
		if !ok {
			t.Fatal("fixture lost its data object", packet.Facts)
		}
		shown, _ := data["entries"].(map[string]any)
		if len(shown) >= len(entries) {
			t.Fatal("requested entries are already complete in the ordinary preview")
		}
		decision := agentcontract.Decision{Kind: "final", AnswerMarkdown: "All 80 entries were returned.", FactIDs: []string{factID}}
		if calls == 2 {
			if packet.CompletionReview == nil {
				t.Fatal("proposal did not enter final-only review")
			}
			decision.CompletionFeedback = "Inspect the saved entries before reporting the complete result."
		}
		return decision, nil
	})
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "running" || state.AnswerMarkdown != "" || len(state.Pending) != 0 || calls != 2 {
		t.Fatal("incomplete evidence was published or dispatched an action", state)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored agentcontract.RuntimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if packet.CompletionReview != nil {
			t.Fatal("inspection remained in final-only review")
		}
		last := packet.Observations[len(packet.Observations)-1]
		if last.ErrorCode == nil || *last.ErrorCode != "completion_incomplete" {
			t.Fatal("missing-evidence feedback was lost across the checkpoint", last)
		}
		return agentcontract.Decision{Kind: "inspect_fact", FactID: factID, Path: []any{"data", "entries"}}, nil
	})
	if err := r.Step(t.Context(), &restored, nil); err != nil {
		t.Fatal(err)
	}
	preview, ok := restored.InspectedFact["preview"].(map[string]any)
	if !ok {
		t.Fatal("inspection did not expose a preview", restored.InspectedFact)
	}
	value, ok := preview["value"].(map[string]any)
	omitted, omittedOK := restored.InspectedFact["omitted_paths"].([][]any)
	if !ok || len(value) != 80 || !omittedOK || len(omitted) != 0 || len(restored.Pending) != 0 {
		t.Fatal("saved inspection did not retain the complete authorized object", restored.InspectedFact)
	}
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if packet.InspectedFact == nil {
			t.Fatal("the complete evidence did not reach the model")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "All 80 entries were returned.", FactIDs: []string{factID}}, nil
	})
	if err := r.Step(t.Context(), &restored, nil); err != nil {
		t.Fatal(err)
	}
	provider, ok := r.Provider.(*coreTestProvider)
	if !ok || provider.called != 0 || restored.Status != "completed" || restored.AnswerMarkdown != "All 80 entries were returned." || len(restored.Pending) != 0 || restored.ModelUsage.Requests != 5 {
		t.Fatal("inspection feedback replayed a business write or lost completion/accounting", restored, provider)
	}
}
