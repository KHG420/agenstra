package agent

import "testing"

func TestSubagentDefinitionProducesDetachedBoundedWorkflow(t *testing.T) {
	definition := SubagentDefinition{Name: "specialist", Version: "1", Description: "Focused record lookup", SystemPrompt: "Specialist instructions", Capabilities: []string{"records.get"}, Skills: []string{"records"}}
	workflow, err := definition.Workflow()
	if err != nil || !workflow.Subagent || len(workflow.Steps) != 1 {
		t.Fatal(workflow, err)
	}
	definition.Capabilities[0], definition.Skills[0] = "records.write", "other"
	if workflow.Steps[0].Agent.Capabilities[0] != "records.get" || workflow.Steps[0].Agent.Skills[0] != "records" {
		t.Fatal("workflow retained caller-owned slices")
	}
	workflow.Steps[0].Agent.OutputSchema = JSON{"type": "object"}
	if workflow.Validate() == nil {
		t.Fatal("specialist output bypassed the evidence-return contract")
	}
	definition.SystemPrompt = ""
	if _, err := definition.Workflow(); err == nil {
		t.Fatal("specialist inherited an unspecified parent prompt")
	}
}
