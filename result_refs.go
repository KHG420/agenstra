package agenstra

import (
	"encoding/json"
	"fmt"
	"math"
)

// ResultRefRequest names evidence; only the server resolves its business ID.
type ResultRefRequest struct {
	FactID     string `json:"fact_id"`
	Path       []any  `json:"path"`
	Label      string `json:"label,omitempty"`
	EntityType string `json:"entity_type,omitempty"`
}

// UnmarshalJSON strictly decodes a requested reference to retained evidence.
func (r *ResultRefRequest) UnmarshalJSON(raw []byte) error {
	type shape ResultRefRequest
	var parsed shape
	if err := strictUnmarshal(raw, &parsed); err != nil {
		return err
	}
	*r = ResultRefRequest(parsed)
	return nil
}

// ResultObjectRef binds an external result identity to a cited fact and declared path.
type ResultObjectRef struct {
	FactID     string `json:"fact_id"`
	Path       []any  `json:"path"`
	ID         string `json:"id"`
	Label      string `json:"label,omitempty"`
	EntityType string `json:"entity_type,omitempty"`
}

func validResultRefRequest(ref ResultRefRequest) bool {
	if !validUUID(ref.FactID) || len(ref.Path) < 1 || len(ref.Path) > 16 || len(ref.Label) > 100 || (ref.EntityType != "" && !fieldPattern.MatchString(ref.EntityType)) {
		return false
	}
	for _, part := range ref.Path {
		switch value := part.(type) {
		case string:
			if value == "" || len(value) > 128 {
				return false
			}
		case int, float64, json.Number:
			index, ok := pathIndex(part)
			if !ok || index < 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func resolveResultRefs(requests []ResultRefRequest, cited []string, facts map[string]Fact, connectionID string) ([]ResultObjectRef, error) {
	allowed := map[string]bool{}
	for _, id := range cited {
		allowed[id] = true
	}
	refs := make([]ResultObjectRef, 0, len(requests))
	for _, request := range requests {
		fact, exists := facts[request.FactID]
		if !validResultRefRequest(request) || !allowed[request.FactID] || !exists || !ReferenceAvailable(fact, connectionID) {
			return nil, fmt.Errorf("final_result_refs_invalid")
		}
		value, err := valueAt(modelFactValue(fact), request.Path)
		if err != nil {
			return nil, fmt.Errorf("final_result_refs_invalid")
		}
		var id string
		switch v := value.(type) {
		case string:
			id = v
		case json.Number:
			id = v.String()
		case int:
			id = fmt.Sprint(v)
		case int64:
			id = fmt.Sprint(v)
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || math.Abs(v) > 9007199254740991 {
				return nil, fmt.Errorf("final_result_refs_invalid")
			}
			id = fmt.Sprintf("%.0f", v)
		default:
			return nil, fmt.Errorf("final_result_refs_invalid")
		}
		if id == "" || len(id) > 256 {
			return nil, fmt.Errorf("final_result_refs_invalid")
		}
		refs = append(refs, ResultObjectRef{FactID: request.FactID, Path: request.Path, ID: id, Label: request.Label, EntityType: request.EntityType})
	}
	return refs, nil
}
