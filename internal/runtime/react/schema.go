package react

import (
	"errors"
	"fmt"
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// inputValidationFeedback exposes bounded contract locations and messages,
// without the validator's absolute local schema URL.
func inputValidationFeedback(schema agentcontract.JSON, arguments agentcontract.JSON) string {
	compiled, err := agentcontract.ValidateLocalSchema(schema, false)
	if err != nil {
		return ""
	}
	var validation *jsonschema.ValidationError
	if !errors.As(agentcontract.ValidateSchema(compiled, arguments), &validation) {
		return ""
	}
	parts := []string{}
	for _, detail := range validation.BasicOutput().Errors {
		if detail.Error != nil {
			parts = append(parts, fmt.Sprintf("at %q: %s", detail.InstanceLocation, detail.Error.String()))
			if len(parts) == 4 {
				break
			}
		}
	}
	text := []rune(strings.Join(parts, "; "))
	if len(text) > 1000 {
		text = text[:1000]
	}
	return string(text)
}
