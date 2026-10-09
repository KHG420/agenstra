package agent

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBrowserCatalogKeepsExecutionHintsWithoutRepeatingHostPollConfiguration(t *testing.T) {
	binding := &OperationBinding{IDPath: []any{"command_id"}, StatusPath: []any{"status"}, PollCapability: "ui.command_status", PollArgument: []string{"command_id"}, PendingStates: []string{"queued", "dispatched", "running"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed", "expired", "cancelled"}, ReconciliationStates: []string{"unknown"}, ReconcileOnTimeout: true, IntervalSeconds: 1, TimeoutSeconds: 60}
	capability := CapabilityDescription{Name: "ui.read_activity", InputSchema: JSON{"type": "object"}, Effect: "read", Operation: binding}
	before, callErr8 := json.Marshal(capability)
	if callErr8 != nil {
		t.Error(callErr8)
	}
	view := capability.ModelView()
	operation := view["operation"].(map[string]any)
	if len(operation) != 1 || operation["poll_capability"] != "ui.command_status" || view["requires_browser_context"] != true {
		t.Fatal("catalog must describe the context prerequisite and result source concisely", view)
	}
	after, callErr9 := json.Marshal(capability)
	if callErr9 != nil {
		t.Error(callErr9)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("model catalog changed the provider contract")
	}
	capability.Name, capability.Operation.PollCapability = "job.start", "job.status"
	if _, ok := capability.ModelView()["operation"].(map[string]any)["poll_argument"]; !ok {
		t.Fatal("non-browser operation details were omitted")
	}
}
