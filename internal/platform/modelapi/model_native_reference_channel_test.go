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
	var batch agentcontract.JSON
	for _, tool := range payload["tools"].([]any) {
		function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		if function["name"] == "tool_batch" {
			batch = function
		}
	}
	if batch == nil {
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
	prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
	if !strings.Contains(prompt, "one-entry tool_batch") || !strings.Contains(prompt, "original resolved input schema") ||
		!strings.Contains(batch["description"].(string), "$fact_value") || !strings.Contains(batch["description"].(string), "single call") ||
		!strings.Contains(business["description"].(string), "symbolic Fact reference") {
		t.Fatal("native tool guidance does not distinguish symbolic proposals from resolved business parameters")
	}
	after, err := agentcontract.CanonicalJSON(packet)
	if err != nil || string(after) != string(raw) {
		t.Fatal("proposal guidance mutated the caller's contracts", err)
	}
}
