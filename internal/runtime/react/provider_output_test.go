package react

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestExecuteCallValidatesCustomProviderOutput(t *testing.T) {
	output := agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
		"count": agentcontract.JSON{"type": "integer", "minimum": 0},
	}, "required": []string{"count"}, "additionalProperties": false}
	for _, tc := range []struct {
		name string
		data agentcontract.JSON
	}{
		{"missing field", agentcontract.JSON{}},
		{"wrong type", agentcontract.JSON{"count": "0"}},
		{"fractional count", agentcontract.JSON{"count": 1.5}},
		{"negative count", agentcontract.JSON{"count": -1}},
		{"extra private field", agentcontract.JSON{"count": 0, "credential": "synthetic-private-value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := agentcontract.CapabilityDescription{Name: "records.get", Effect: "read", OutputSchema: output, ModelOutput: &agentcontract.ModelOutput{Paths: [][]string{}}}
			p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, invoke: func(agentcontract.JSON) agentcontract.CapabilityResult {
				return agentcontract.CapabilityResult{Data: tc.data}
			}}
			outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{}}, nil)
			if err != nil || outcome.ErrorCode != "upstream_response_invalid" || outcome.Fact != nil || p.called != 1 {
				t.Fatalf("invalid output became successful evidence: %+v, error=%v, calls=%d", outcome, err, p.called)
			}
		})
	}
	for _, declared := range []agentcontract.JSON{output, nil} {
		cap := agentcontract.CapabilityDescription{Name: "records.get", OutputSchema: declared}
		data := agentcontract.JSON{"count": 0}
		p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, invoke: func(agentcontract.JSON) agentcontract.CapabilityResult {
			return agentcontract.CapabilityResult{Data: data}
		}}
		outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{}}, nil)
		if err != nil || outcome.Fact == nil || outcome.ErrorCode != "" || p.called != 1 {
			t.Fatal("valid zero or undeclared output schema rejected", outcome, err, p.called)
		}
		data["count"] = 7
		if outcome.Fact.Value["data"].(agentcontract.JSON)["count"] != 0 {
			t.Fatal("validated output retained provider-owned data")
		}
	}
	t.Run("malformed declared output schema", func(t *testing.T) {
		cap := agentcontract.CapabilityDescription{Name: "records.get", OutputSchema: agentcontract.JSON{"type": "invalid-type"}}
		p := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
		outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{}}, nil)
		if err != nil || outcome.ErrorCode != "upstream_response_invalid" || outcome.Fact != nil || p.called != 1 {
			t.Fatal("malformed output contract became a success", outcome, err, p.called)
		}
	})
	t.Run("declared provider failure remains a failure", func(t *testing.T) {
		cap := agentcontract.CapabilityDescription{Name: "records.get", OutputSchema: output}
		p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, invoke: func(agentcontract.JSON) agentcontract.CapabilityResult {
			return agentcontract.CapabilityResult{ErrorCode: "business_not_found"}
		}}
		outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{}}, nil)
		if err != nil || outcome.ErrorCode != "business_not_found" || outcome.Fact != nil || p.called != 1 {
			t.Fatal("output schema replaced the actual failure", outcome, err, p.called)
		}
	})
}

func TestTransientRuntimeRejectsCustomProviderOutputBeforeRecovery(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.get", Effect: "read", OutputSchema: agentcontract.JSON{
		"type": "object", "properties": agentcontract.JSON{"count": agentcontract.JSON{"type": "integer"}}, "required": []string{"count"},
	}}
	p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}}
	p.invoke = func(agentcontract.JSON) agentcontract.CapabilityResult {
		if p.called == 1 {
			return agentcontract.CapabilityResult{Data: agentcontract.JSON{"count": "0"}}
		}
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"count": 0}}
	}
	r := &AgentRuntime{Provider: p, Grants: map[string]bool{cap.Name: true}}
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if len(packet.Observations) == 0 {
			return agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "first-1", Capability: cap.Name, Arguments: agentcontract.JSON{}, Reason: "Read"}}}, nil
		}
		first := packet.Observations[0]
		if first.Status != "failed" || first.ErrorCode == nil || *first.ErrorCode != "upstream_response_invalid" {
			t.Fatal("invalid result reached model as success", packet.Observations)
		}
		if len(packet.Facts) == 0 {
			return agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "retry-1", Capability: cap.Name, Arguments: agentcontract.JSON{}, Reason: "Retry the read"}}}, nil
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "The count is zero.", FactIDs: []string{packet.Facts[0].FactID}}, nil
	})
	result, err := r.Run(t.Context(), "Read the count")
	if err != nil || result.Status != "completed" || p.called != 2 || len(result.Facts) != 1 || len(result.Observations) != 2 || result.Observations[1].Status != "succeeded" {
		t.Fatal("output rejection or read recovery failed", result, err, p.called)
	}
}
