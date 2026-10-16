package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeReferenceProposalChannelMatchesDeclaredSchemas(t *testing.T) {
	packet := nativeTestPacket()
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	model := &HTTPJSONDecisionModel{Model: "any-compatible-model", DecisionOutputMode: "native_tools"}
	payload, err := model.requestPayload(raw, "Preserve host authorization and requested evidence sources.")
	if err != nil {
		t.Fatal(err)
	}
	business := nativeTestFunction(t, payload, "inventory.get")
	var batch, single agentcontract.JSON
	for _, tool := range payload["tools"].([]any) {
		function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		if function["name"] == "tool_batch" {
			batch = function
		} else if function["name"] == "tool_call" {
			single = function
		}
	}
	if batch == nil || single == nil {
		t.Fatal("existing proposal function unavailable")
	}
	args := agentcontract.JSON{"id": agentcontract.JSON{"$fact_value": agentcontract.JSON{
		"fact_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "path": []any{"data", "id"},
	}}}
	directSchema, err := agentcontract.ValidateLocalSchema(business["parameters"].(agentcontract.JSON), false)
	if err != nil || agentcontract.ValidateSchema(directSchema, args) == nil {
		t.Fatal("business schema must retain its original resolved parameter contract", err)
	}
	batchSchema, err := agentcontract.ValidateLocalSchema(batch["parameters"].(agentcontract.JSON), false)
	proposal := agentcontract.JSON{"calls": []any{agentcontract.JSON{
		"capability": "inventory.get", "arguments": args, "reason": "Use the requested recorded source",
	}}}
	if err != nil || agentcontract.ValidateSchema(batchSchema, proposal) != nil {
		t.Fatal("a one-call symbolic proposal is not represented by the existing batch schema", err)
	}
	singleSchema, err := agentcontract.ValidateLocalSchema(single["parameters"].(agentcontract.JSON), false)
	if err != nil || agentcontract.ValidateSchema(singleSchema, proposal["calls"].([]any)[0]) != nil {
		t.Fatal("a flat symbolic proposal does not match its declared schema", err)
	}
	prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
	if !strings.Contains(prompt, "tool_call performs exactly one business invocation") || !strings.Contains(prompt, "original resolved input schema") ||
		!strings.Contains(batch["description"].(string), "$fact_value") || !strings.Contains(single["description"].(string), "exactly one business call") ||
		!strings.Contains(business["description"].(string), "symbolic Fact reference") {
		t.Fatal("native tool guidance does not distinguish symbolic proposals from resolved business parameters")
	}
	after, err := agentcontract.CanonicalJSON(packet)
	if err != nil || string(after) != string(raw) {
		t.Fatal("proposal guidance mutated the caller's contracts", err)
	}
}

func TestNativeReferenceProposalFunctionsRequireDeclaredCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name         string
		capabilities []agentcontract.JSON
	}{
		{name: "empty"},
		{name: "deferred", capabilities: []agentcontract.JSON{{"name": "inventory.deferred", "schema_requires_inspection": true}}},
		{name: "unauthorized", capabilities: []agentcontract.JSON{{"name": "inventory.get", "authorized": false, "input_schema": agentcontract.JSON{"type": "object"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packet := nativeTestPacket()
			packet.Capabilities = tc.capabilities
			raw, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			model := &HTTPJSONDecisionModel{Model: "any-compatible-model", DecisionOutputMode: "native_tools"}
			payload, err := model.requestPayload(raw, "Preserve current capability availability.")
			if err != nil {
				t.Fatal(err)
			}
			for _, tool := range payload["tools"].([]any) {
				name := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)["name"]
				if name == "tool_call" || name == "tool_batch" {
					t.Fatal("proposal function was declared without a callable contract", name)
				}
			}
		})
	}
}
