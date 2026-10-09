package host

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type frozenSelectionModel struct {
	snapshot agentcontract.ModelSelectionSnapshot
	selected []agentcontract.ModelConfiguration
}

func (m *frozenSelectionModel) Snapshot() agentcontract.ModelSelectionSnapshot { return m.snapshot }
func (m *frozenSelectionModel) SelectModel(c agentcontract.ModelConfiguration) (agentcontract.DecisionModel, error) {
	m.selected = append(m.selected, c)
	return &hostModel{}, nil
}
func (*frozenSelectionModel) Decide(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
	return agentcontract.Decision{}, agentcontract.NewHostError("unfrozen_model_used")
}

func TestHostFreezesModelSelectionThroughInterface(t *testing.T) {
	model := &frozenSelectionModel{snapshot: agentcontract.ModelSelectionSnapshot{Revision: 3, Config: agentcontract.ModelConfiguration{
		DefaultProfile: "decision", MemoryExtractionProfile: "memory", Profiles: map[string]agentcontract.ModelProfile{
			"decision": {Model: "original-decision"}, "memory": {Model: "original-memory"}, "unused": {Model: "unused"},
		},
	}}}
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	h.Model = model
	run := createTestHostRun(t, h)
	if len(model.selected) != 1 || len(model.selected[0].Profiles) != 2 {
		t.Fatalf("create did not freeze used profiles: %+v", model.selected)
	}
	// Live deployment changes must not reroute an existing run or its memory model.
	model.snapshot.Revision = 4
	model.snapshot.Config.Profiles["decision"] = agentcontract.ModelProfile{Model: "new-decision"}
	model.snapshot.Config.Profiles["memory"] = agentcontract.ModelProfile{Model: "new-memory"}
	restored := NewAgentHost(h.Store, h.ProviderFactory, model, h.PolicyResolver)
	run, err := restored.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatalf("restored run: %+v %v", run, err)
	}
	if len(model.selected) < 2 {
		t.Fatal("restored host did not select a frozen model")
	}
	for _, c := range model.selected {
		if c.Profiles["decision"].Model != "original-decision" || c.Profiles["memory"].Model != "original-memory" || len(c.Profiles) != 2 {
			t.Fatalf("run selection changed: %+v", c)
		}
	}
}
