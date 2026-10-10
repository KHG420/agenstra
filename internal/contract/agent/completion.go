package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
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
	if err := jsonvalue.DecodeStrict(raw, &parsed); err != nil {
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
				if index, ok := jsonvalue.Index(p); !ok || index < 0 {
					return nil, fmt.Errorf("completion requirement path invalid")
				}
			}
		}
		schema, err := ValidateLocalSchema(JSON{"const": requirement.Value}, false)
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
				value, err := ValueAt(fact.Value, path)
				if err != nil || ValidateSchema(schema, value) != nil {
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

// EvaluationCase specifies one explicit evaluation request and evidence requirements.
type EvaluationCase struct {
	Name                  string            `json:"name"`
	PackID                string            `json:"pack_id"`
	Instruction           string            `json:"instruction"`
	RunID                 string            `json:"run_id,omitempty"`
	RequestID             string            `json:"request_id,omitempty"`
	ExpectedStatus        string            `json:"expected_status,omitempty"`
	RequiredCapabilities  []string          `json:"required_capabilities,omitempty"`
	ForbiddenCapabilities []string          `json:"forbidden_capabilities,omitempty"`
	Facts                 []FactRequirement `json:"facts,omitempty"`
}

// EvaluationCheck records whether a requested evidence assertion passed.
type EvaluationCheck struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Passed bool   `json:"passed"`
	Code   string `json:"code,omitempty"`
}

// EvaluationResult combines persisted run evidence, diagnostics and evaluation checks.
type EvaluationResult struct {
	Name        string            `json:"name"`
	Iteration   int               `json:"iteration,omitempty"`
	ElapsedMS   int64             `json:"elapsed_ms,omitempty"`
	RunID       string            `json:"run_id,omitempty"`
	RequestID   string            `json:"request_id,omitempty"`
	Status      string            `json:"status,omitempty"`
	Passed      bool              `json:"passed"`
	Checks      []EvaluationCheck `json:"checks"`
	ErrorCode   string            `json:"error_code,omitempty"`
	Diagnostics *RunDiagnostics   `json:"diagnostics,omitempty"`
}

// Validate rejects unsupported evaluation scope and requirements before execution.
func (c EvaluationCase) Validate() error {
	if len(c.RequestID) > 128 {
		return fmt.Errorf("evaluation request_id too long")
	}
	if c.Name == "" || (c.RunID == "" && (c.PackID == "" || c.Instruction == "")) || (c.RunID != "" && !ValidUUID(c.RunID)) {
		return fmt.Errorf("evaluation case identity invalid")
	}
	status := c.ExpectedStatus
	if status == "" {
		status = "completed"
	}
	switch status {
	case "completed", "failed", "cancelled", "needs_input", "needs_approval", "needs_authorization", "needs_reconciliation":
	default:
		return fmt.Errorf("evaluation expected_status invalid")
	}
	if len(c.Facts) > 0 && status != "completed" {
		return fmt.Errorf("fact checks require completed status")
	}
	if _, err := RequireFactValues(c.Facts...); err != nil {
		return err
	}
	for _, names := range [][]string{c.RequiredCapabilities, c.ForbiddenCapabilities} {
		for _, name := range names {
			if name == "" {
				return fmt.Errorf("evaluation capability required")
			}
		}
	}
	return nil
}

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
	if err := jsonvalue.DecodeStrict(raw, &parsed); err != nil {
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

// ValidResultRefRequest checks a model result-reference request before resolving it against evidence.
func ValidResultRefRequest(ref ResultRefRequest) bool {
	if !ValidUUID(ref.FactID) || len(ref.Path) < 1 || len(ref.Path) > 16 || utf8.RuneCountInString(ref.Label) > 100 || (ref.EntityType != "" && !FieldPattern.MatchString(ref.EntityType)) {
		return false
	}
	for _, part := range ref.Path {
		switch value := part.(type) {
		case string:
			if value == "" || len(value) > 128 {
				return false
			}
		case int, float64, json.Number:
			index, ok := jsonvalue.Index(part)
			if !ok || index < 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
