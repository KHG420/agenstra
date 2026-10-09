package react

import (
	"slices"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// inspectionView uses the same visibility rules for a new inspection and a
// retained one. Expired references remain readable evidence, as with Fact views.
func inspectionView(facts map[string]agentcontract.Fact, id string, path []any) (agentcontract.JSON, error) {
	path = slices.Clone(path)
	selected, err := ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": id, "path": path}}, facts, "", false)
	if err != nil {
		return nil, err
	}
	fact := facts[id]
	fact.Value = agentcontract.JSON{"value": selected}
	fact.ModelOutput = nil
	view := factView(fact, 6000)
	item := agentcontract.JSON{"fact_id": id, "path": path, "preview": view.Value, "omitted_paths": view.OmittedPaths}
	if a, ok := selected.([]any); ok {
		item["array_length"] = len(a)
	}
	for i := len(path) - 1; i >= 0; i-- {
		if index, ok := jsonvalue.Index(path[i]); ok {
			parent, e := agentcontract.ValueAt(agentcontract.ModelFactValue(facts[id]), path[:i])
			if e == nil {
				if a, ok := parent.([]any); ok {
					item["parent_array_length"] = len(a)
					item["inspected_index"] = index
				}
			}
			break
		}
	}
	return item, nil
}

// inspectionHistory is a projection of the existing journal, not new execution
// state. Re-resolving each path also rechecks model_output after restoration.
func inspectionHistory(state *agentcontract.RuntimeState) ([]agentcontract.JSON, int) {
	facts := make(map[string]agentcontract.Fact, len(state.Facts))
	for _, fact := range state.Facts {
		facts[fact.FactID] = fact
	}
	key := func(id string, path []any) string {
		raw, err := agentcontract.CanonicalJSON(agentcontract.JSON{"fact_id": id, "path": append([]any{}, path...)})
		if err != nil {
			return ""
		}
		return string(raw)
	}
	seen := map[string]bool{}
	if id, ok := state.InspectedFact["fact_id"].(string); ok {
		path, _ := state.InspectedFact["path"].([]any)
		seen[key(id, path)] = true
	}
	history := []agentcontract.JSON{}
	omitted := 0
	for i := len(state.Decisions) - 1; i >= 0; i-- {
		recorded := state.Decisions[i]
		if recorded["kind"] != "inspect_fact" {
			continue
		}
		id, _ := recorded["fact_id"].(string)
		path, ok := recorded["path"].([]any)
		if !ok && recorded["path"] != nil {
			continue
		}
		decision := agentcontract.Decision{Kind: "inspect_fact", FactID: id, Path: path}
		if decision.Validate() != nil {
			continue
		}
		identity := key(id, path)
		if identity == "" || seen[identity] {
			continue
		}
		seen[identity] = true
		view, err := inspectionView(facts, id, path)
		if err != nil {
			continue
		}
		if len(history) < 12 {
			history = append(history, view)
		} else {
			omitted++
		}
	}
	slices.Reverse(history)
	return history, omitted
}
