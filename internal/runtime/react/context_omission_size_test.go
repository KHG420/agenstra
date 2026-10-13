package react

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestObjectOmissionsPrioritizeParentWithoutChangingPaths(t *testing.T) {
	entries := agentcontract.JSON{}
	paths := [][]any{}
	for i := range 20 {
		key := fmt.Sprintf("item-%02d", i)
		entries[key] = agentcontract.JSON{"value": i}
		paths = append(paths, []any{"data", "entries", key})
	}
	paths = append(paths, []any{"data", "entries"})
	fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"entries": entries}})
	view := agentcontract.FactView{Fact: fact, OmittedPaths: paths}
	before, err := agentcontract.CanonicalJSON(view)
	if err != nil {
		t.Fatal(err)
	}
	notes := arrayOmissions([]agentcontract.Fact{fact}, []agentcontract.FactView{view})
	want := fmt.Sprintf("fact %s: object at [\"data\",\"entries\"] has 20 fields; preview may omit fields; inspect_fact before reporting full object coverage", fact.FactID)
	if len(notes) != 12 || notes[0] != want {
		t.Fatal("whole omitted object was lost behind child hints or the note bound changed", notes)
	}
	after, err := agentcontract.CanonicalJSON(view)
	if err != nil || string(after) != string(before) {
		t.Fatal("sorting omission hints changed the original preview", err)
	}
}

func TestObjectOmissionsUseOnlyAuthorizedProjectionAndKeepArrayLengths(t *testing.T) {
	fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{
		"visible": agentcontract.JSON{"rows": []any{1, 2, 3}, "zero": 0, "null": nil},
		"private": agentcontract.JSON{"hidden": 1, "other": 2},
	}})
	fact.ModelOutput = &agentcontract.ModelOutput{Paths: [][]string{{"visible", "rows"}, {"visible", "zero"}}}
	view := factView(fact, 0)
	notes := strings.Join(arrayOmissions([]agentcontract.Fact{fact}, []agentcontract.FactView{view}), "\n")
	for _, want := range []string{
		`object at [] has 1 fields`,
		`object at ["data"] has 1 fields`,
		`object at ["data","visible"] has 2 fields`,
		`array at ["data","visible","rows"] has 3 items`,
	} {
		if !strings.Contains(notes, want) {
			t.Fatal("projected collection size is missing", want, notes)
		}
	}
	if strings.Contains(notes, "private") || strings.Contains(notes, "hidden") || strings.Contains(notes, `,"null"`) {
		t.Fatal("omission size hints exposed hidden fields", notes)
	}
	if got := arrayOmissions([]agentcontract.Fact{fact}, []agentcontract.FactView{factView(fact, 6000)}); len(got) != 0 {
		t.Fatal("a complete projected result was marked incomplete", got)
	}
}

func TestObjectOmissionsPreserveScalarFallback(t *testing.T) {
	fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"body": strings.Repeat("x", 10000)}})
	view := agentcontract.FactView{Fact: fact, OmittedPaths: [][]any{{"data", "body"}}}
	notes := arrayOmissions([]agentcontract.Fact{fact}, []agentcontract.FactView{view})
	if !reflect.DeepEqual(notes, []string{"fact omitted_paths are unknown/incomplete; inspect_fact before asserting null/empty values"}) {
		t.Fatal("scalar omission was treated as a complete collection", notes)
	}
}

func TestObjectOmissionsDoNotDisplaceArrayLengths(t *testing.T) {
	entries := agentcontract.JSON{}
	for i := range 20 {
		entries[fmt.Sprintf("item-%02d", i)] = agentcontract.JSON{"value": i}
	}
	fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"entries": entries, "rows": []any{1, 2, 3}}})
	view := agentcontract.FactView{Fact: fact, OmittedPaths: [][]any{{"data", "entries"}, {"data", "rows"}}}
	notes := arrayOmissions([]agentcontract.Fact{fact}, []agentcontract.FactView{view})
	if len(notes) != 12 || !strings.Contains(notes[0], `array at ["data","rows"] has 3 items`) ||
		!strings.Contains(notes[1], `object at ["data","entries"] has 20 fields`) {
		t.Fatal("object hints displaced an existing array length or lost their shallow-path priority", notes)
	}
}

func TestObjectOmissionsReachDecisionWithinBudgetWithoutChangingEvidence(t *testing.T) {
	for _, allowance := range []int{2200, 16000} {
		t.Run(fmt.Sprint(allowance), func(t *testing.T) {
			r, state, provider := contextBudgetRuntime(t, allowance)
			entries := agentcontract.JSON{}
			for i := range 68 {
				entries[fmt.Sprintf("item-%02d", i)] = agentcontract.JSON{"value": strings.Repeat("x", 150)}
			}
			fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"entries": entries}})
			state.Facts = []agentcontract.Fact{fact}
			before, err := agentcontract.CanonicalJSON(state.Facts)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			r.Model = contextBudgetModel(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
				calls++
				assertContextBudget(t, packet, prompt, r.MaxContextCharacters)
				if allowance == 16000 {
					want := `object at ["data","entries"] has 68 fields`
					if !strings.Contains(prompt, "Authoritative full object size") || !strings.Contains(prompt, want) ||
						!strings.Contains(prompt, "Copying a partial preview does not satisfy a request") ||
						!strings.Contains(prompt, "Unrelated omissions do not block an answer") {
						t.Fatal("first decision did not receive full object size guidance", prompt)
					}
				}
				return agentcontract.Decision{Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "entries", "item-67"}}, nil
			})
			if err := r.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			after, err := agentcontract.CanonicalJSON(state.Facts)
			if err != nil || string(after) != string(before) || calls != 1 || provider.called != 0 {
				t.Fatal("guidance changed evidence, replayed a business call or blocked a fitting input", err, calls, provider.called)
			}
			value, err := agentcontract.ValueAt(state.InspectedFact, []any{"preview", "value", "value"})
			if err != nil || value != strings.Repeat("x", 150) {
				t.Fatal("the model could not inspect the original missing value", value, err)
			}
		})
	}
}
