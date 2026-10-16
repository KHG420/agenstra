package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeDiscoveryFunctionsDescribeMetadataWithoutBusinessIO(t *testing.T) {
	packet := nativeTestPacket()
	packet.RuntimeFeatures = []string{"capability_search"}
	input, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: "native_tools"}
	payload, err := model.requestPayload(input, "Preserve the current authorization and original task.")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"search_capabilities": {"current authorized capability catalog", "visible catalog is incomplete", "no business API call", "does not by itself establish a host limitation or a permission denial"},
		"inspect_capability":  {"complete input and output contract", "one exact capability name", "no business API call", "capability_unknown", "never grants permission"},
	}
	for _, tool := range payload["tools"].([]any) {
		function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		name := function["name"].(string)
		for _, rule := range want[name] {
			if !strings.Contains(function["description"].(string), rule) {
				t.Errorf("%s discovery semantics missing from its tool definition: %s", name, rule)
			}
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatal("runtime discovery functions unavailable", want)
	}
	if after, err := agentcontract.CanonicalJSON(packet); err != nil || string(after) != string(input) {
		t.Fatal("discovery descriptions mutated caller context", err)
	}
}
