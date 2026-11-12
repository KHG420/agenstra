package host

import (
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRunModelWrapperReceivesTrustedBinding(t *testing.T) {
	model := &hostModel{}
	h := testHost(t, testStore(t), &hostProvider{}, model)
	run, err := h.Create(t.Context(), "alice", "records", "read records", "")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	h.RunModelWrapper = func(binding agentcontract.RunModelContext, selected agentcontract.DecisionModel) agentcontract.DecisionModel {
		called = true
		if binding.OwnerID != "alice" || binding.RunID != run.RunID || binding.PackID != "records" || selected != model {
			t.Fatal("wrapper lost the stored run identity", binding)
		}
		return selected
	}
	selected, err := h.modelForRun(run)
	if err != nil || selected != model || !called {
		t.Fatal("model decoration failed", err)
	}
	h.RunModelWrapper = func(agentcontract.RunModelContext, agentcontract.DecisionModel) agentcontract.DecisionModel {
		return nil
	}
	_, err = h.modelForRun(run)
	var failure *agentcontract.HostError
	if !errors.As(err, &failure) || failure.Code != "model_unavailable" {
		t.Fatal("nil decorated model was accepted", err)
	}
}
