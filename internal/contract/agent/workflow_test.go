package agent

import (
	"strings"
	"testing"
)

func TestWorkflowDefinitionValidationAndReferences(t *testing.T) {
	valid := WorkflowDefinition{Name: "report", Version: "1", Description: "Report", InputSchema: JSON{"type": "object"}, Steps: []WorkflowStep{{ID: "read", Capability: "records.read", Arguments: JSON{"id": JSON{"$workflow": "/input/id"}}}, {ID: "save", Capability: "records.save", DependsOn: []string{"read"}, Arguments: JSON{"id": JSON{"$workflow": "/steps/read/data/id"}}}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*WorkflowDefinition){
		func(w *WorkflowDefinition) { w.Steps[0].DependsOn = []string{"save"} },
		func(w *WorkflowDefinition) { w.Steps[1].DependsOn = nil },
		func(w *WorkflowDefinition) { w.Steps[0].Capability = "workflows.start" },
		func(w *WorkflowDefinition) { w.Steps[0].Agent = &WorkflowAgent{Instruction: "Conflicting node"} },
		func(w *WorkflowDefinition) { w.Steps[0].ID = "save" },
	} {
		copy := valid
		copy.Steps = append([]WorkflowStep{}, valid.Steps...)
		change(&copy)
		if err := copy.Validate(); err == nil {
			t.Fatal("invalid workflow accepted", copy)
		}
	}
	for _, path := range []string{"/input/a~1b/~0key", "/steps/read/data/0"} {
		if _, err := WorkflowPointer(path); err != nil {
			t.Fatal(path, err)
		}
	}
	for _, path := range []string{"https://remote.example/value", "/input/~2", "/input/~"} {
		if _, err := WorkflowPointer(path); err == nil {
			t.Fatal(path)
		}
	}
}

func TestWorkflowExpansionIsBoundedAndCopiesReferencedData(t *testing.T) {
	value := JSON{"nested": JSON{"value": "original"}}
	resolved, err := ResolveWorkflowValue(JSON{"first": JSON{"$workflow": "/input/value"}}, func(string) (any, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	resolved.(map[string]any)["first"].(map[string]any)["nested"].(map[string]any)["value"] = "changed"
	if value["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("reference mutated input")
	}
	if _, err := ResolveWorkflowValue(JSON{"a": JSON{"$workflow": "/input/a"}, "b": JSON{"$workflow": "/input/b"}}, func(string) (any, error) { return strings.Repeat("x", 700000), nil }); ErrorCode(err) != "workflow value: workflow_value_too_large" {
		if err == nil {
			t.Fatal("unbounded expansion accepted")
		}
	}
}
