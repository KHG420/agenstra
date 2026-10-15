package react

import (
	"bytes"
	"fmt"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCapabilityVisibilityPolicyDoesNotDisplaceCompletedInspections(t *testing.T) {
	runtime, state, provider := contextBudgetRuntime(t, 8000)
	runtime.MaxContextCapabilities = 1
	paths := [][]string{{"result", "id"}, {"result", "enabled"}, {"result", "missing"}}
	for i := 0; i < 200; i++ {
		paths = append(paths, []string{"result", fmt.Sprintf("optional_declared_property_with_long_name_%03d", i)})
	}
	capability := agentcontract.CapabilityDescription{Name: "record.read", Version: "1", InputSchema: agentcontract.JSON{"type": "object"},
		OutputSchema: agentcontract.JSON{"type": "object", "description": "Original output contract"},
		ModelOutput:  &agentcontract.ModelOutput{Paths: paths}, Effect: "read", Replay: "safe"}
	provider.caps[capability.Name] = capability
	runtime.Grants[capability.Name] = true
	fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"result": agentcontract.JSON{"id": 23, "enabled": false, "missing": nil}, "credential": "private-sentinel"}})
	fact.ModelOutput = capability.ModelOutput
	state.Facts = []agentcontract.Fact{fact}
	state.InspectedCapability = agentcontract.Strptr(capability.Name)
	state.Decisions = []agentcontract.JSON{
		{"kind": "inspect_capability", "name": capability.Name},
		{"kind": "inspect_fact", "fact_id": fact.FactID, "path": []any{"data", "result", "id"}},
		{"kind": "inspect_fact", "fact_id": fact.FactID, "path": []any{"data", "result"}},
	}
	var err error
	state.InspectedFact, err = inspectionView(map[string]agentcontract.Fact{fact.FactID: fact}, fact.FactID, []any{"data", "result"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	contractBefore, err := agentcontract.CanonicalJSON(provider.caps[capability.Name])
	if err != nil {
		t.Fatal(err)
	}
	packet := runtime.Context(state)
	assertContextBudget(t, packet, runtime.SystemPrompt(), runtime.MaxContextCharacters)
	if len(packet.InspectionHistory) != 1 || packet.InspectionHistory[0]["fact_id"] != fact.FactID {
		t.Fatal("runtime visibility policy displaced a completed ID inspection")
	}
	wire, err := agentcontract.CanonicalJSON(packet)
	if err != nil || bytes.Contains(wire, []byte("model_output")) || bytes.Contains(wire, []byte("private-sentinel")) {
		t.Fatal("model context contains execution visibility policy or private result data", err)
	}
	wantContract, err := agentcontract.ObjectOf(capability)
	if err != nil {
		t.Fatal(err)
	}
	delete(wantContract, "model_output")
	want, err := agentcontract.CanonicalJSON(wantContract)
	actual, actualErr := agentcontract.CanonicalJSON(packet.InspectedCapability)
	if err != nil || actualErr != nil || !bytes.Equal(want, actual) {
		t.Fatal("policy omission changed the complete inspected input/output contract", err, actualErr)
	}
	_, err = ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"data", "credential"}}}, map[string]agentcontract.Fact{fact.FactID: fact}, "", true)
	if err == nil {
		t.Fatal("private Fact reference was allowed after model-context policy omission")
	}
	after, err := agentcontract.CanonicalJSON(state)
	contractAfter, contractErr := agentcontract.CanonicalJSON(provider.caps[capability.Name])
	if err != nil || contractErr != nil || !bytes.Equal(before, after) || !bytes.Equal(contractBefore, contractAfter) {
		t.Fatal("context projection mutated complete evidence or execution policy", err, contractErr)
	}
}
