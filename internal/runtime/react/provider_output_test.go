package react

import (
	"context"
	"strings"
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
		if err != nil || outcome.ErrorCode != "upstream_response_invalid" || outcome.Fact != nil || p.called != 0 {
			t.Fatal("malformed output contract reached provider", outcome, err, p.called)
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

func TestExecuteCallRejectsMalformedCustomProviderResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result agentcontract.CapabilityResult
		code   string
	}{
		{"raw error text", agentcontract.CapabilityResult{ErrorCode: "Authorization: Bearer synthetic-private-value"}, "upstream_response_invalid"},
		{"control character", agentcontract.CapabilityResult{ErrorCode: "business_failed\nprivate"}, "upstream_response_invalid"},
		{"oversized error code", agentcontract.CapabilityResult{ErrorCode: strings.Repeat("x", 121)}, "upstream_response_invalid"},
		{"data and failure", agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "R-1"}, ErrorCode: "business_failed"}, "upstream_response_invalid"},
		{"empty data and failure", agentcontract.CapabilityResult{Data: agentcontract.JSON{}, ErrorCode: "business_failed"}, "upstream_response_invalid"},
		{"neither result channel", agentcontract.CapabilityResult{}, "upstream_response_invalid"},
		{"valid business failure", agentcontract.CapabilityResult{ErrorCode: "business_not_found"}, "business_not_found"},
		{"maximum valid code length", agentcontract.CapabilityResult{ErrorCode: strings.Repeat("x", 120)}, strings.Repeat("x", 120)},
		{"valid provider code", agentcontract.CapabilityResult{ErrorCode: "provider.limit:429-v1"}, "provider.limit:429-v1"},
		{"uncertain provider outcome", agentcontract.CapabilityResult{ErrorCode: "provider_outcome_unknown"}, "provider_outcome_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := agentcontract.CapabilityDescription{Name: "records.get", Effect: "read"}
			p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, invoke: func(agentcontract.JSON) agentcontract.CapabilityResult { return tc.result }}
			outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{}}, nil)
			if err != nil || outcome.ErrorCode != tc.code || outcome.Fact != nil || p.called != 1 {
				t.Fatal("malformed or valid failure channel handled incorrectly", tc.name, err, p.called)
			}
		})
	}
}

func TestMalformedProviderErrorDoesNotEnterModelOrSavedState(t *testing.T) {
	const privateText = "Authorization: Bearer synthetic-private-value"
	cap := agentcontract.CapabilityDescription{Name: "records.get", Effect: "read"}
	p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, invoke: func(agentcontract.JSON) agentcontract.CapabilityResult {
		return agentcontract.CapabilityResult{ErrorCode: privateText}
	}}
	r := &AgentRuntime{Provider: p, Grants: map[string]bool{cap.Name: true}}
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		raw, err := agentcontract.CanonicalJSON(packet)
		if err != nil || strings.Contains(string(raw)+prompt, privateText) {
			t.Fatal("malformed provider error reached the model")
		}
		if len(packet.Observations) == 0 {
			return agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "read-1", Capability: cap.Name, Arguments: agentcontract.JSON{}, Reason: "Read"}}}, nil
		}
		if first := packet.Observations[0]; first.ErrorCode == nil || *first.ErrorCode != "upstream_response_invalid" || first.FactID != nil || len(packet.Facts) != 0 {
			t.Fatal("malformed response became a definite failure or success evidence")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "The query returned an invalid response.", FactIDs: []string{}}, nil
	})
	state, err := r.Run(t.Context(), "Read once and report any error")
	if err != nil || state.Status != "completed" || p.called != 1 {
		t.Fatal("malformed-response read could not finish", err, p.called)
	}
	raw, err := agentcontract.CanonicalJSON(state)
	if err != nil || strings.Contains(string(raw), privateText) {
		t.Fatal("malformed provider error reached saved state")
	}
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
