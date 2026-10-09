package react

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestModelOutputEmptyAndLegacyBehavior(t *testing.T) {
	full := agentcontract.JSON{"data": agentcontract.JSON{"id": "R1", "secret": "private-token"}}
	legacy := agentcontract.Fact{FactID: agentcontract.NewID(), Value: full}
	if got := factView(legacy, 6000).Value["data"].(map[string]any)["secret"]; got != "private-token" {
		t.Fatalf("nil model_output changed legacy preview: %v", got)
	}
	opaque := agentcontract.Fact{FactID: agentcontract.NewID(), Value: full, ModelOutput: &agentcontract.ModelOutput{Paths: [][]string{}}}
	if got := factView(opaque, 6000).Value["data"].(map[string]any); len(got) != 0 {
		t.Fatalf("empty paths exposed result: %v", got)
	}
	_, err := ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": opaque.FactID, "path": []any{"data", "id"}}}, map[string]agentcontract.Fact{opaque.FactID: opaque}, "", true)
	if err == nil || err.Error() != "fact_reference_path_invalid" {
		t.Fatalf("opaque result reference accepted: %v", err)
	}
}

func TestCustomProviderRejectsInvalidModelOutputBeforeInvoke(t *testing.T) {
	for _, output := range []*agentcontract.ModelOutput{{Paths: nil}, {Paths: [][]string{{}}}, {Paths: [][]string{{""}}}} {
		cap := agentcontract.CapabilityDescription{Name: "record.read", ModelOutput: output}
		provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
		outcome, err := ExecuteCall(context.Background(), provider, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{}}, nil)
		if err != nil || outcome.Fact != nil || outcome.ErrorCode != "model_output_config_invalid" || provider.called != 0 {
			t.Fatalf("invalid custom projection reached provider or fact: %+v %v calls=%d", outcome, err, provider.called)
		}
		fact := agentcontract.Fact{Value: agentcontract.JSON{"data": agentcontract.JSON{"secret": "private"}}, ModelOutput: output}
		if visible := agentcontract.ModelFactValue(fact)["data"].(map[string]any); len(visible) != 0 {
			t.Fatalf("invalid persisted projection exposed data: %v", visible)
		}
	}
	for _, output := range []*agentcontract.ModelOutput{nil, {Paths: [][]string{}}} {
		cap := agentcontract.CapabilityDescription{Name: "record.read", ModelOutput: output}
		provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
		outcome, err := ExecuteCall(context.Background(), provider, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{}}, nil)
		if err != nil || outcome.Fact == nil || provider.called != 1 {
			t.Fatalf("valid custom projection rejected: %+v %v calls=%d", outcome, err, provider.called)
		}
	}
}
