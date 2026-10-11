package modelapi

import (
	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// nativeDecisionOutputSchema uses the same available decision variants as tool
// output, retaining the original JSON decision identity and local call references.
// Optional fields and open business arguments require non-strict provider mode.
func nativeDecisionOutputSchema(tools []any) agentcontract.JSON {
	schema := singleDecisionOutputTool(tools)["function"].(agentcontract.JSON)["parameters"].(agentcontract.JSON)
	identity := agentcontract.JSON{"type": "string", "const": "agenstra.decision.v1"}
	schema["properties"].(agentcontract.JSON)["schema"] = identity
	schema["required"] = append(schema["required"].([]string), "schema")
	for _, value := range schema["anyOf"].([]any) {
		variant := value.(agentcontract.JSON)
		properties := variant["properties"].(agentcontract.JSON)
		properties["schema"] = identity
		variant["required"] = append(variant["required"].([]string), "schema")
		if calls, ok := properties["calls"].(agentcontract.JSON); ok {
			call := calls["items"].(agentcontract.JSON)
			call["required"] = append(call["required"].([]string), "call_ref")
			call["properties"].(agentcontract.JSON)["call_ref"].(agentcontract.JSON)["description"] = "Fresh local call reference matching the declared pattern; do not reuse a recorded call reference."
		}
	}
	return schema
}

// validateNativeDecisionOutput treats a provider's schema output as untrusted.
// The ordinary Decision and runtime authorization checks still follow.
func validateNativeDecisionOutput(body []byte, schema agentcontract.JSON) string {
	if detail := modelJSONFormatError(body); detail != "" {
		return detail
	}
	var value agentcontract.JSON
	if err := jsonvalue.DecodeStrict(body, &value); err != nil {
		return "model_decision_schema_invalid"
	}
	compiled, err := agentcontract.ValidateLocalSchema(schema, false)
	if err != nil {
		return "model_decision_schema_invalid"
	}
	if err := agentcontract.ValidateSchema(compiled, value); err != nil {
		return "model_decision_schema_invalid"
	}
	return ""
}
