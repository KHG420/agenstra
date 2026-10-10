package react

import (
	"errors"
	"fmt"
	"slices"
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
	// The original full contract already rejected the input. Narrow only its
	// diagnostic: unrelated tagged branches must not consume the error budget.
	narrowed, feedback := taggedInputDiagnostic(compiled, arguments)
	if feedback == "" && narrowed != compiled {
		var selected *jsonschema.ValidationError
		if errors.As(agentcontract.ValidateSchema(narrowed, arguments), &selected) {
			validation = selected
		}
	}
	parts := []string{}
	if feedback != "" {
		parts = append(parts, feedback)
	} else {
		for _, detail := range validation.BasicOutput().Errors {
			if detail.Error != nil {
				parts = append(parts, fmt.Sprintf("at %q: %s", detail.InstanceLocation, detail.Error.String()))
				if len(parts) == 4 {
					break
				}
			}
		}
	}
	text := []rune(strings.Join(parts, "; "))
	if len(text) > 1000 {
		text = text[:1000]
	}
	return string(text)
}

// taggedInputDiagnostic recognizes a root object union with unique required
// string constants. Other unions retain the validator's original diagnostics.
// The copied compiled root retains all sibling constraints and local refs.
func taggedInputDiagnostic(compiled *jsonschema.Schema, arguments agentcontract.JSON) (*jsonschema.Schema, string) {
	branches := compiled.AnyOf
	if len(branches) == 0 {
		branches = compiled.OneOf
	} else if len(compiled.OneOf) != 0 {
		return compiled, ""
	}
	if len(branches) < 2 {
		return compiled, ""
	}
	names := slices.Clone(branches[0].Required)
	slices.Sort(names)
	for _, name := range names {
		value, present := arguments[name].(string)
		if !present {
			continue
		}
		seen := map[string]bool{}
		var selected *jsonschema.Schema
		for _, branch := range branches {
			property := branch.Properties[name]
			if branch.Types == nil || !slices.Equal(branch.Types.ToStrings(), []string{"object"}) ||
				!slices.Contains(branch.Required, name) || property == nil || property.Const == nil {
				break
			}
			tag, ok := (*property.Const).(string)
			if !ok || seen[tag] {
				break
			}
			seen[tag] = true
			if tag == value {
				selected = branch
			}
		}
		if len(seen) != len(branches) {
			continue
		}
		if selected == nil {
			path := "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
			return compiled, fmt.Sprintf("at %q: discriminator value is not supported by this capability; inspect its declared variants or another authorized capability before constructing arguments. This rejection did not execute the business operation and does not prove that the host lacks it.", path)
		}
		narrowed := *compiled
		if len(compiled.AnyOf) != 0 {
			narrowed.AnyOf = []*jsonschema.Schema{selected}
		} else {
			narrowed.OneOf = []*jsonschema.Schema{selected}
		}
		return &narrowed, ""
	}
	return compiled, ""
}
