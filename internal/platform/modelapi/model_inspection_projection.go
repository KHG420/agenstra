package modelapi

import (
	"slices"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// Keep the complete inspected subtree once, omitting its older partial copy.
// Other fields, array positions, provenance and the caller's evidence stay intact.
func completeInspectionInput(packet agentcontract.ContextPacket) ([]byte, error) {
	// A complete page covers only a range, not the whole subtree at path.
	// Removing that subtree would hide other already-visible array positions.
	if _, ranged := packet.InspectedFact["offset"]; ranged {
		return nil, nil
	}
	path, pathOK := packet.InspectedFact["path"].([]any)
	omitted, omissionOK := packet.InspectedFact["omitted_paths"].([]any)
	if !pathOK || !omissionOK || len(omitted) != 0 {
		return nil, nil
	}
	for i, fact := range packet.Facts {
		if fact.FactID != packet.InspectedFact["fact_id"] {
			continue
		}
		if _, err := agentcontract.ValueAt(fact.Value, path); err != nil {
			return nil, nil
		}
		incomplete, covered := false, false
		for _, missing := range fact.OmittedPaths {
			n := min(len(path), len(missing))
			a, err := agentcontract.CanonicalJSON(path[:n])
			if err != nil {
				return nil, err
			}
			b, err := agentcontract.CanonicalJSON(missing[:n])
			if err != nil {
				return nil, err
			}
			if string(a) == string(b) {
				incomplete = true
				covered = covered || len(missing) <= len(path)
			}
		}
		if !incomplete {
			return nil, nil
		}
		raw, err := agentcontract.CanonicalJSON(fact)
		if err != nil {
			return nil, err
		}
		var view agentcontract.FactView
		if err := jsonvalue.DecodeStrict(raw, &view); err != nil {
			return nil, err
		}
		if len(path) == 0 {
			view.Value = agentcontract.JSON{}
		} else {
			parent, err := agentcontract.ValueAt(view.Value, path[:len(path)-1])
			if err != nil {
				return nil, nil
			}
			switch parent := parent.(type) {
			case map[string]any:
				key, ok := path[len(path)-1].(string)
				if !ok {
					return nil, nil
				}
				delete(parent, key)
			case []any:
				index, ok := jsonvalue.Index(path[len(path)-1])
				if !ok || index < 0 || index >= len(parent) {
					return nil, nil
				}
				parent[index] = nil
			default:
				return nil, nil
			}
		}
		if !covered {
			view.OmittedPaths = append(view.OmittedPaths, slices.Clone(path))
		}
		original, err := agentcontract.CanonicalJSON(packet)
		if err != nil {
			return nil, err
		}
		packet.Facts = slices.Clone(packet.Facts)
		packet.Facts[i] = view
		compact, err := agentcontract.CanonicalJSON(packet)
		if err != nil {
			return nil, err
		}
		if len(compact) < len(original) {
			return compact, nil
		}
		return nil, nil
	}
	return nil, nil
}
