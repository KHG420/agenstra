package engine

import (
	"errors"
	"fmt"
	"strings"

	localschema "github.com/KHG420/agenstra/internal/contract/schema"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func validateLocalSchema(schema JSON, strictREST bool) (*jsonschema.Schema, error) {
	return localschema.CompileLocal(schema, strictREST)
}
func validateSchema(s *jsonschema.Schema, v any) error {
	return localschema.Validate(s, v)
}

// inputValidationFeedback exposes bounded contract locations and messages,
// without the validator's absolute local schema URL.
func inputValidationFeedback(schema JSON, arguments JSON) string {
	compiled, err := validateLocalSchema(schema, false)
	if err != nil {
		return ""
	}
	var validation *jsonschema.ValidationError
	if !errors.As(validateSchema(compiled, arguments), &validation) {
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
