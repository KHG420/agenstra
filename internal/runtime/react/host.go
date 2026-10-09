package react

import (
	"encoding/json"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// UnknownOutcome recognizes errors that leave the external execution result uncertain.
func UnknownOutcome(code string) bool {
	return code == "provider_outcome_unknown" || code == "upstream_response_invalid" || code == "upstream_unavailable"
}

// OperationValue reads a persisted external operation path, including numeric array steps.
func OperationValue(value any, path []any) (any, error) {
	for _, p := range path {
		switch v := value.(type) {
		case map[string]any:
			key, ok := p.(string)
			if !ok {
				return nil, agentcontract.NewHostError("operation_contract_invalid")
			}
			var exists bool
			value, exists = v[key]
			if !exists {
				return nil, agentcontract.NewHostError("operation_contract_invalid")
			}
		case []any:
			var index int
			switch x := p.(type) {
			case int:
				index = x
			case float64:
				if x != float64(int(x)) {
					return nil, agentcontract.NewHostError("operation_contract_invalid")
				}
				index = int(x)
			case json.Number:
				i, e := x.Int64()
				if e != nil {
					return nil, agentcontract.NewHostError("operation_contract_invalid")
				}
				index = int(i)
			default:
				return nil, agentcontract.NewHostError("operation_contract_invalid")
			}
			if index < 0 || index >= len(v) {
				return nil, agentcontract.NewHostError("operation_contract_invalid")
			}
			value = v[index]
		default:
			return nil, agentcontract.NewHostError("operation_contract_invalid")
		}
	}
	return value, nil
}
