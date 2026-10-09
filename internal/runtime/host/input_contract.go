package host

import (
	"time"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// ValidateRequestedInput checks a user answer against the saved input request.
// A nil schema retains the existing unconstrained text behavior.
func ValidateRequestedInput(schema *agentcontract.RequestedInputSchema, text string) error {
	if schema == nil {
		return nil
	}
	if schema.Validate() != nil {
		return agentcontract.NewHostError("run_state_invalid")
	}
	switch schema.Type {
	case "string":
		length := utf8.RuneCountInString(text)
		if length < schema.MinLength || (schema.MaxLength > 0 && length > schema.MaxLength) {
			return agentcontract.NewHostError("input_invalid")
		}
	case "enum":
		for _, option := range schema.Enum {
			if text == option {
				return nil
			}
		}
		return agentcontract.NewHostError("input_invalid")
	case "date":
		parsed, err := time.Parse("2006-01-02", text)
		if err != nil || parsed.Format("2006-01-02") != text {
			return agentcontract.NewHostError("input_invalid")
		}
	}
	return nil
}
