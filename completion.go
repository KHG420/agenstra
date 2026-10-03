package agenstra

import (
	"context"
	"encoding/json"
	"fmt"
)

// CompletionContext is supplied only after citation identity checks succeed.
// Validators must be read-only and respect cancellation. Facts are complete,
// rather than the bounded previews supplied to the model.
type CompletionContext struct {
	RunID, OriginPackID, Instruction, AnswerMarkdown string
	FactIDs                                          []string
	Facts                                            []Fact
	Observations                                     []Observation
	Followups                                        []string
}

// CompletionValidator checks cited business evidence after citation identities are verified.
// It must respect cancellation and return deliberate feedback through CompletionValidationError.
type CompletionValidator func(context.Context, CompletionContext) error

// CompletionValidationError carries deliberately model-visible feedback.
// Ordinary errors are reduced to a stable code and never expose their text.
type CompletionValidationError struct{ Kind, Feedback string }

// Error returns the safe error identifier.
func (e CompletionValidationError) Error() string { return e.Kind }

// Code exposes the stable identifier used by framework error handling.
func (e CompletionValidationError) Code() string { return e.Kind }

// FactRequirement constrains the latest cited value from one capability.
type FactRequirement struct {
	Capability string `json:"capability"`
	Path       []any  `json:"path"`
	Value      any    `json:"value"`
	Required   bool   `json:"required,omitempty"`
}

// UnmarshalJSON strictly decodes a requirement and requires an explicit value.
func (r *FactRequirement) UnmarshalJSON(raw []byte) error {
	type requirement FactRequirement
	var parsed requirement
	if err := strictUnmarshal(raw, &parsed); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if _, ok := fields["value"]; !ok {
		return fmt.Errorf("fact requirement value required")
	}
	*r = FactRequirement(parsed)
	return nil
}

// RequireFactValues checks the latest evidence from each required capability,
// its citation and a JSON value. JSON Schema equality preserves numeric semantics.
// A host can compose this validator with domain-specific answer validation.
func RequireFactValues(requirements ...FactRequirement) (CompletionValidator, error) {
	checks := make([]CompletionValidator, 0, len(requirements))
	for _, requirement := range requirements {
		if requirement.Capability == "" || len(requirement.Path) > 16 {
			return nil, fmt.Errorf("completion requirement invalid")
		}
		for _, p := range requirement.Path {
			if _, ok := p.(string); !ok {
				if index, ok := pathIndex(p); !ok || index < 0 {
					return nil, fmt.Errorf("completion requirement path invalid")
				}
			}
		}
		schema, err := validateLocalSchema(JSON{"const": requirement.Value}, false)
		if err != nil {
			return nil, err
		}
		capability, path := requirement.Capability, append([]any{}, requirement.Path...)
		checks = append(checks, func(ctx context.Context, result CompletionContext) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			feedback := fmt.Sprintf("Cite the latest successful Fact from %s with the required value at %v.", capability, path)
			for i := len(result.Facts) - 1; i >= 0; i-- {
				fact := result.Facts[i]
				if fact.SourceCapability != capability {
					continue
				}
				cited := false
				for _, id := range result.FactIDs {
					cited = cited || id == fact.FactID
				}
				if !cited {
					return CompletionValidationError{"completion_evidence_missing", feedback}
				}
				value, err := valueAt(fact.Value, path)
				if err != nil || validateSchema(schema, value) != nil {
					return CompletionValidationError{"completion_evidence_mismatch", feedback}
				}
				for _, observation := range result.Observations {
					if observation.FactID != nil && *observation.FactID == fact.FactID {
						for _, later := range result.Observations {
							if later.CallRef == observation.CallRef && later.ErrorCode != nil && *later.ErrorCode == "operation_failed" {
								return CompletionValidationError{"completion_operation_failed", feedback}
							}
						}
					}
				}
				return nil
			}
			return CompletionValidationError{"completion_evidence_missing", feedback}
		})
	}
	return func(ctx context.Context, result CompletionContext) error {
		for _, check := range checks {
			if err := check(ctx, result); err != nil {
				return err
			}
		}
		return ctx.Err()
	}, nil
}
