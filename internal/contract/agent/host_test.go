package agent

import (
	"encoding/json"
	"testing"
)

func callDecision(name string) Decision {
	return Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "lookup-1", Capability: name, Arguments: JSON{"id": "R-1"}, Reason: "Look up record"}}}
}

func TestCanonicalJSONCompatibility(t *testing.T) {
	b, e := CanonicalJSON(map[string]any{"z": json.Number("1.0"), "a": "记录 <A>&", "nested": map[string]any{"b": 2, "a": 1}})
	want := `{"a":"记录 <A>&","nested":{"a":1,"b":2},"z":1.0}`
	if e != nil || string(b) != want {
		t.Fatalf("canonical %s %v", b, e)
	}
}
