package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

var workflowName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

// WorkflowDefinition is a trusted, versioned DAG of existing capabilities and Agent steps.
// Arguments, output and answer accept {"$workflow":"/input/field"} or
// {"$workflow":"/steps/step_id/data/field"} JSON Pointer references.
type WorkflowDefinition struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	InputSchema JSON           `json:"input_schema"`
	Steps       []WorkflowStep `json:"steps"`
	Output      JSON           `json:"output,omitempty"`
	Answer      any            `json:"answer,omitempty"`
}

// WorkflowStep binds one existing capability or one bounded Agent task.
// Dependencies must include every referenced step, directly or transitively.
type WorkflowStep struct {
	ID         string             `json:"id"`
	DependsOn  []string           `json:"depends_on,omitempty"`
	Capability string             `json:"capability,omitempty"`
	Agent      *WorkflowAgent     `json:"agent,omitempty"`
	Arguments  JSON               `json:"arguments,omitempty"`
	When       *WorkflowCondition `json:"when,omitempty"`
}

// WorkflowAgent selects a task and a subset of the current user's tools.
// An empty capabilities list runs analysis without tool access.
type WorkflowAgent struct {
	Instruction  string   `json:"instruction"`
	Capabilities []string `json:"capabilities"`
	OutputSchema JSON     `json:"output_schema,omitempty"`
}

// WorkflowCondition compares a resolved value with a literal JSON value.
type WorkflowCondition struct {
	Value  any `json:"value"`
	Equals any `json:"equals"`
}

// WorkflowExecution is stored in the existing run checkpoint with its immutable definition.
type WorkflowExecution struct {
	Definition      WorkflowDefinition  `json:"definition"`
	Input           JSON                `json:"input"`
	Steps           []WorkflowStepState `json:"steps"`
	Output          JSON                `json:"output,omitempty"`
	AgentModelCalls []ModelCallMetrics  `json:"agent_model_calls,omitempty"`
}

// WorkflowStepState retains node progress and references existing run evidence.
type WorkflowStepState struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	FactID    string `json:"fact_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Output    JSON   `json:"output,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

// WorkflowParent restricts an internally created run to its parent's live scope.
type WorkflowParent struct {
	RunID        string   `json:"run_id"`
	StepID       string   `json:"step_id,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// WorkflowInfo is the authorized catalog entry disclosed to clients and models.
type WorkflowInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	InputSchema JSON   `json:"input_schema"`
}

// WorkflowView exposes business progress, results and the run requiring user action.
// Detailed invocations and approvals remain on the existing owner-scoped run API.
type WorkflowView struct {
	RunID        string              `json:"run_id"`
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Status       string              `json:"status"`
	Steps        []WorkflowStepState `json:"steps"`
	Output       JSON                `json:"output,omitempty"`
	Answer       string              `json:"answer_markdown,omitempty"`
	ErrorCode    *string             `json:"error_code,omitempty"`
	BlockedRunID string              `json:"blocked_run_id,omitempty"`
	BlockedState string              `json:"blocked_state,omitempty"`
}

// Validate rejects ambiguous nodes, cycles, unbounded definitions and invalid references.
func (w WorkflowDefinition) Validate() error {
	bad := func() error { return NewHostError("workflow_definition_invalid") }
	raw, err := CanonicalJSON(w)
	if err != nil || len(raw) > 256<<10 || !workflowName.MatchString(w.Name) || len(w.Version) < 1 || len(w.Version) > 64 || len(w.Description) > 4000 || len(w.Steps) < 1 || len(w.Steps) > 64 {
		return bad()
	}
	if w.InputSchema == nil {
		return bad()
	}
	if _, err := ValidateLocalSchema(w.InputSchema, false); err != nil {
		return bad()
	}
	steps := map[string]WorkflowStep{}
	for _, step := range w.Steps {
		if !workflowName.MatchString(step.ID) || steps[step.ID].ID != "" || (step.Capability == "") == (step.Agent == nil) || len(step.Capability) > 256 || strings.HasPrefix(step.Capability, "workflows.") || len(step.DependsOn) > 64 {
			return bad()
		}
		if step.Agent != nil {
			if strings.TrimSpace(step.Agent.Instruction) == "" || len(step.Agent.Instruction) > 12000 || len(step.Agent.Capabilities) > 200 {
				return bad()
			}
			for _, name := range step.Agent.Capabilities {
				if name == "" || strings.HasPrefix(name, "workflows.") {
					return bad()
				}
			}
			if step.Agent.OutputSchema != nil {
				if _, err := ValidateLocalSchema(step.Agent.OutputSchema, false); err != nil {
					return bad()
				}
			}
		}
		steps[step.ID] = step
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if done[id] {
			return nil
		}
		step, ok := steps[id]
		if !ok || visiting[id] {
			return bad()
		}
		visiting[id] = true
		seen := map[string]bool{}
		for _, dependency := range step.DependsOn {
			if seen[dependency] {
				return bad()
			}
			seen[dependency] = true
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id], done[id] = false, true
		return nil
	}
	for id := range steps {
		if err := visit(id); err != nil {
			return err
		}
	}
	var ancestors func(string, map[string]bool)
	ancestors = func(id string, allowed map[string]bool) {
		for _, dependency := range steps[id].DependsOn {
			if !allowed[dependency] {
				allowed[dependency] = true
				ancestors(dependency, allowed)
			}
		}
	}
	for _, step := range w.Steps {
		allowed := map[string]bool{}
		ancestors(step.ID, allowed)
		if err := validateWorkflowReferences(step.Arguments, allowed); err != nil {
			return bad()
		}
		if step.When != nil {
			if err := validateWorkflowReferences(step.When.Value, allowed); err != nil {
				return bad()
			}
		}
	}
	all := map[string]bool{}
	for id := range steps {
		all[id] = true
	}
	if validateWorkflowReferences(w.Output, all) != nil || validateWorkflowReferences(w.Answer, all) != nil {
		return bad()
	}
	return nil
}

func validateWorkflowReferences(value any, allowed map[string]bool) error {
	_, err := ResolveWorkflowValue(value, func(pointer string) (any, error) {
		parts, err := WorkflowPointer(pointer)
		if err != nil || len(parts) == 0 {
			return nil, NewHostError("workflow_reference_invalid")
		}
		if parts[0] == "input" {
			return nil, nil
		}
		if parts[0] != "steps" || len(parts) < 2 || !allowed[parts[1]] {
			return nil, NewHostError("workflow_reference_invalid")
		}
		return nil, nil
	})
	return err
}

// WorkflowPointer parses a bounded RFC 6901 pointer without accepting URI or remote references.
func WorkflowPointer(pointer string) ([]string, error) {
	if !strings.HasPrefix(pointer, "/") || len(pointer) > 2048 {
		return nil, NewHostError("workflow_reference_invalid")
	}
	parts := strings.Split(pointer[1:], "/")
	if len(parts) > 18 {
		return nil, NewHostError("workflow_reference_invalid")
	}
	for i, part := range parts {
		for offset := 0; offset < len(part); offset++ {
			if part[offset] == '~' {
				offset++
				if offset == len(part) || (part[offset] != '0' && part[offset] != '1') {
					return nil, NewHostError("workflow_reference_invalid")
				}
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// ResolveWorkflowValue copies a JSON template and resolves only its explicit workflow references.
func ResolveWorkflowValue(value any, resolve func(string) (any, error)) (any, error) {
	encoded, err := CanonicalJSON(value)
	if err != nil || len(encoded) > 1<<20 {
		return nil, NewHostError("workflow_value_too_large")
	}
	normalized, err := jsonvalue.Clone(value)
	if err != nil {
		return nil, NewHostError("workflow_reference_invalid")
	}
	var walk func(any, int) (any, error)
	remaining := (1 << 20) - len(encoded)
	walk = func(value any, depth int) (any, error) {
		if depth > 24 {
			return nil, NewHostError("workflow_reference_invalid")
		}
		switch typed := value.(type) {
		case map[string]any:
			if _, exists := typed["$fact_value"]; exists {
				return nil, NewHostError("workflow_reference_invalid")
			}
			if _, exists := typed["$fact_id"]; exists {
				return nil, NewHostError("workflow_reference_invalid")
			}
			if ref, exists := typed["$workflow"]; exists {
				pointer, ok := ref.(string)
				if !ok || len(typed) != 1 {
					return nil, NewHostError("workflow_reference_invalid")
				}
				resolved, err := resolve(pointer)
				if err != nil {
					return nil, err
				}
				raw, err := CanonicalJSON(resolved)
				if err != nil || len(raw) > remaining {
					return nil, NewHostError("workflow_value_too_large")
				}
				remaining -= len(raw)
				return jsonvalue.Clone(resolved)
			}
			for key, child := range typed {
				result, err := walk(child, depth+1)
				if err != nil {
					return nil, err
				}
				typed[key] = result
			}
		case []any:
			for i, child := range typed {
				result, err := walk(child, depth+1)
				if err != nil {
					return nil, err
				}
				typed[i] = result
			}
		}
		return value, nil
	}
	result, err := walk(normalized, 0)
	if err != nil {
		return nil, fmt.Errorf("workflow value: %w", err)
	}
	return result, nil
}
