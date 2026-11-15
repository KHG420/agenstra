package agent

import "strings"

// SubagentDefinition declares an isolated specialist called through workflows.start.
// Its declared tools and skills are exposed only within the specialist context.
// Its tools remain a subset of the originating owner's live grants. Execution
// uses the existing durable workflow, child-run, cancellation and budget paths.
type SubagentDefinition struct {
	Name         string
	Version      string
	Description  string
	SystemPrompt string
	Capabilities []string
	Skills       []string
}

// Workflow returns a detached single-agent workflow accepting one task string.
// The completed output contains the specialist's answer and cited evidence.
func (s SubagentDefinition) Workflow() (WorkflowDefinition, error) {
	if strings.TrimSpace(s.SystemPrompt) == "" {
		return WorkflowDefinition{}, NewHostError("workflow_definition_invalid")
	}
	ref := func(path string) JSON { return JSON{"$workflow": path} }
	w := WorkflowDefinition{
		Subagent: true, Name: s.Name, Version: s.Version, Description: s.Description,
		InputSchema: JSON{"type": "object", "properties": JSON{"task": JSON{"type": "string", "minLength": 1, "maxLength": 12000}}, "required": []string{"task"}, "additionalProperties": false},
		Steps: []WorkflowStep{{ID: "agent", Agent: &WorkflowAgent{
			Instruction: "Complete the delegated task.", SystemPrompt: s.SystemPrompt,
			Capabilities: append([]string{}, s.Capabilities...), Skills: append([]string{}, s.Skills...),
		}, Arguments: JSON{"task": ref("/input/task")}}},
		Output: JSON{"answer_markdown": ref("/steps/agent/answer_markdown"), "evidence": ref("/steps/agent/evidence")},
		Answer: ref("/steps/agent/answer_markdown"),
	}
	return w, w.Validate()
}
