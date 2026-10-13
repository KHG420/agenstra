package react

import (
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestObjectOmissionHintsNameOnlyAuthorizedMissingKeys(t *testing.T) {
	fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{
		"visible": agentcontract.JSON{"known": 1, "required_zero": 0, "required_null": nil, "required_false": false, "large_key_" + strings.Repeat("k", 2000): "private-value-marker"},
		"private": agentcontract.JSON{"hidden_key_marker": "private-value-marker"},
	}})
	fact.ModelOutput = &agentcontract.ModelOutput{Paths: [][]string{{"visible"}}}
	view := factView(fact, 0)
	view.Value = agentcontract.JSON{"data": agentcontract.JSON{"visible": agentcontract.JSON{"known": 1}}}
	view.OmittedPaths = [][]any{{"data", "visible"}}
	notes := strings.Join(arrayOmissions([]agentcontract.Fact{fact}, []agentcontract.FactView{view}), "\n")
	for _, name := range []string{"required_zero", "required_null", "required_false"} {
		if !strings.Contains(notes, name) {
			t.Fatal("stored missing key was mistaken for an absent field", name, notes)
		}
	}
	for _, hidden := range []string{"hidden_key_marker", "private-value-marker", strings.Repeat("k", 2000)} {
		if strings.Contains(notes, hidden) {
			t.Fatal("key hints exposed hidden data or an oversized field name")
		}
	}
	if !strings.Contains(notes, "values unknown") || strings.Contains(notes, `"known"`) {
		t.Fatal("omitted key hints asserted values or marked a visible key missing", notes)
	}
}

func TestObjectOmissionKeyHintsSurviveTwelveArrayLengths(t *testing.T) {
	arrays := agentcontract.JSON{}
	for i := range 20 {
		arrays[fmt.Sprintf("array-%02d", i)] = []any{1, 2, 3}
	}
	fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{
		"arrays": arrays, "record": agentcontract.JSON{"known": 1, "needed": false},
	}})
	view := factView(fact, 0)
	view.Value = agentcontract.JSON{"data": agentcontract.JSON{"record": agentcontract.JSON{"known": 1}}}
	view.OmittedPaths = [][]any{{"data", "record"}, {"data", "arrays"}}
	notes := arrayOmissions([]agentcontract.Fact{fact}, []agentcontract.FactView{view})
	text := strings.Join(notes, "\n")
	if len(notes) > 24 || strings.Count(text, "array at ") != 12 || !strings.Contains(text, `stored but omitted field names: ["needed"]`) {
		t.Fatal("named omitted fields displaced array lengths or were lost behind them", notes)
	}
}

func TestObjectOmissionHintsCoverDirectPathsBeforeNestedObjects(t *testing.T) {
	facts := []agentcontract.Fact{}
	views := []agentcontract.FactView{}
	for batch := range 2 {
		rows := []any{}
		preview := []any{}
		omitted := [][]any{}
		for i := range 5 {
			rows = append(rows, agentcontract.JSON{
				"id": i, "needed": false,
				"nested-a": agentcontract.JSON{"enabled": true},
				"nested-b": agentcontract.JSON{"enabled": true},
				"nested-c": agentcontract.JSON{"enabled": true},
			})
			preview = append(preview, agentcontract.JSON{"id": i})
			omitted = append(omitted, []any{"data", "items", i})
		}
		fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"items": rows}})
		fact.FactID = fmt.Sprintf("batch-%d", batch)
		view := factView(fact, 0)
		view.Value = agentcontract.JSON{"data": agentcontract.JSON{"items": preview}}
		view.OmittedPaths = omitted
		facts = append(facts, fact)
		views = append(views, view)
	}
	notes := arrayOmissions(facts, views)
	text := strings.Join(notes, "\n")
	for batch := range 2 {
		for i := range 5 {
			path := fmt.Sprintf(`fact batch-%d: object at ["data","items",%d]`, batch, i)
			if !strings.Contains(text, path) {
				t.Fatal("nested object hints displaced a directly omitted record", path, notes)
			}
		}
	}
	if len(notes) > 24 || strings.Count(text, "object at ") > 12 {
		t.Fatal("covering direct paths exceeded the existing hint limit", notes)
	}
}
