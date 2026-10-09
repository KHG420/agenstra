package host

import (
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func (h *AgentHost) modelForRun(run agentcontract.StoredRun) (agentcontract.DecisionModel, error) {
	manager, ok := h.Model.(runModelSelector)
	if !ok {
		return h.Model, nil
	}
	c, err := h.effectiveRunConfig(run)
	if err != nil {
		return nil, err
	}
	if c.ModelSelection == nil {
		return manager.SelectModel(manager.Snapshot().Config)
	}
	return manager.SelectModel(c.ModelSelection.Config)
}
