package host

import (
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func (h *AgentHost) modelForRun(run agentcontract.StoredRun) (agentcontract.DecisionModel, error) {
	model := h.Model
	manager, ok := h.Model.(runModelSelector)
	if !ok && h.RunModelWrapper == nil {
		return model, nil
	}
	c, err := h.effectiveRunConfig(run)
	if err != nil {
		return nil, err
	}
	binding := agentcontract.RunModelContext{OwnerID: run.OwnerID, RunID: run.RunID, PackID: run.PackID}
	if c.ModelSelection != nil {
		binding.Configuration = &c.ModelSelection.Config
	}
	if ok {
		config := manager.Snapshot().Config
		if c.ModelSelection != nil {
			config = c.ModelSelection.Config
		}
		model, err = manager.SelectModel(config)
		if err != nil {
			return nil, err
		}
		binding.Configuration = &config
	}
	if h.RunModelWrapper != nil {
		model = h.RunModelWrapper(binding, model)
		if model == nil {
			return nil, agentcontract.NewHostError("model_unavailable")
		}
	}
	return model, nil
}
