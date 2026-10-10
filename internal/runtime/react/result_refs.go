package react

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func resolveResultRefs(requests []agentcontract.ResultRefRequest, cited []string, facts map[string]agentcontract.Fact, connectionID string) ([]agentcontract.ResultObjectRef, error) {
	allowed := map[string]bool{}
	for _, id := range cited {
		allowed[id] = true
	}
	refs := make([]agentcontract.ResultObjectRef, 0, len(requests))
	for _, request := range requests {
		fact, exists := facts[request.FactID]
		if !agentcontract.ValidResultRefRequest(request) || !allowed[request.FactID] || !exists || !ReferenceAvailable(fact, connectionID) {
			return nil, fmt.Errorf("final_result_refs_invalid")
		}
		value, err := agentcontract.ValueAt(agentcontract.ModelFactValue(fact), request.Path)
		if err != nil {
			return nil, fmt.Errorf("final_result_refs_invalid")
		}
		var id string
		switch v := value.(type) {
		case string:
			id = v
		case json.Number:
			if len(v.String()) > 256 || !json.Valid([]byte(v.String())) {
				return nil, fmt.Errorf("final_result_refs_invalid")
			}
			bounded, err := v.Float64()
			if err != nil {
				return nil, fmt.Errorf("final_result_refs_invalid")
			}
			if bounded == 0 {
				// Distinguish exact zero from a nonzero fraction that underflowed.
				mantissa, _, _ := strings.Cut(strings.ToLower(v.String()), "e")
				if strings.Trim(mantissa, "-0.") != "" {
					return nil, fmt.Errorf("final_result_refs_invalid")
				}
				id = "0"
				break
			}
			// Float64 above only bounds exponent work. Resolve the exact value,
			// without rounding a fractional or large integer business identity.
			exact, ok := new(big.Rat).SetString(v.String())
			if !ok || !exact.IsInt() {
				return nil, fmt.Errorf("final_result_refs_invalid")
			}
			id = exact.Num().String()
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
		refs = append(refs, agentcontract.ResultObjectRef{FactID: request.FactID, Path: request.Path, ID: id, Label: request.Label, EntityType: request.EntityType})
	}
	return refs, nil
}
