package react

import (
	"encoding/json"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestFinalResultReferencesResolveTypedProviderJSON(t *testing.T) {
	type record struct {
		ID      json.Number `json:"id"`
		Private string      `json:"-"`
	}
	type namedRecords map[string]record
	for _, test := range []struct {
		name string
		data agentcontract.JSON
		path []any
	}{
		{"typed_slice", agentcontract.JSON{"records": []record{{ID: "9007199254740993"}}}, []any{"data", "records", 0, "id"}},
		{"typed_struct", agentcontract.JSON{"record": record{ID: "9007199254740993"}}, []any{"data", "record", "id"}},
		{"pointer", agentcontract.JSON{"record": &record{ID: "9007199254740993"}}, []any{"data", "record", "id"}},
		{"named_map", agentcontract.JSON{"records": namedRecords{"selected": {ID: "9007199254740993"}}}, []any{"data", "records", "selected", "id"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			capability := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", Effect: "read"}
			provider := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{capability.Name: capability}}, invoke: func(agentcontract.JSON) agentcontract.CapabilityResult {
				return agentcontract.CapabilityResult{Data: test.data}
			}}
			outcome, err := ExecuteCall(t.Context(), provider, map[string]bool{capability.Name: true}, agentcontract.ToolCall{Capability: capability.Name, Arguments: agentcontract.JSON{}}, nil)
			if err != nil || outcome.Fact == nil {
				t.Fatal("typed provider evidence was rejected", outcome, err)
			}
			fact := *outcome.Fact
			refs, err := resolveResultRefs([]agentcontract.ResultRefRequest{{FactID: fact.FactID, Path: test.path}}, []string{fact.FactID}, map[string]agentcontract.Fact{fact.FactID: fact}, "")
			if err != nil || len(refs) != 1 || refs[0].ID != "9007199254740993" {
				t.Fatal("JSON-visible provider identity could not be referenced exactly", refs, err)
			}
			privatePath := append([]any(nil), test.path...)
			privatePath[len(privatePath)-1] = "Private"
			if _, err := agentcontract.ValueAt(fact.Value, privatePath); err == nil {
				t.Fatal("non-JSON Go state became visible through a reference")
			}
		})
	}
}
