package agenstra

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestOperationBindingDefaultsAndDisjointStates(t *testing.T) {
	var binding OperationBinding
	raw := []byte(`{"id_path":["job_id"],"status_path":["status"],"poll_capability":"job.poll","poll_argument":["job_id"]}`)
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalJSON(binding)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"interval_seconds":5.0`) || !strings.Contains(string(canonical), `"timeout_seconds":3600.0`) {
		t.Fatalf("Python float defaults lost: %s", canonical)
	}
	bad := []byte(`{"id_path":["job_id"],"status_path":["status"],"poll_capability":"job.poll","poll_argument":["job_id"],"pending_states":["same"],"success_states":["same"]}`)
	if err := json.Unmarshal(bad, &binding); err == nil {
		t.Fatal("overlapping states accepted")
	}
}

func TestJSONBoundariesRejectUnsupportedAndCyclicValues(t *testing.T) {
	cycle := JSON{}
	cycle["self"] = cycle
	for _, value := range []any{math.NaN(), math.Inf(1), func() {}, cycle} {
		if _, err := CanonicalJSON(JSON{"value": value}); err == nil {
			t.Errorf("CanonicalJSON accepted %T", value)
		}
		decision := Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "read", Capability: "records.get", Reason: "read", Arguments: JSON{"value": value}}}}
		if err := decision.Validate(); err == nil {
			t.Errorf("Decision.Validate accepted %T", value)
		}
	}
}
