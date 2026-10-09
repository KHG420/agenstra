package host

import (
	"encoding/json"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// NormalizedRunSettings fills legacy zero-valued defaults while preserving configured execution limits.
func NormalizedRunSettings(s agentcontract.HostSettings) agentcontract.HostSettings {
	if s == (agentcontract.HostSettings{}) {
		s = agentcontract.DefaultHostSettings()
	}
	if s.MaxConversationHistoryMessages == 0 {
		s.MaxConversationHistoryMessages = 6
	}
	if s.MaxConversationHistoryCharacters == 0 {
		s.MaxConversationHistoryCharacters = 1500
	}
	if s.MaxConversationMessages == 0 {
		s.MaxConversationMessages = 500
	}
	if s.MaxStagnantRounds == 0 {
		s.MaxStagnantRounds = 8
	}
	if s.MaxConcurrentTools == 0 {
		s.MaxConcurrentTools = 4
	}
	return s
}

func (h *AgentHost) effectiveRunConfig(run agentcontract.StoredRun) (agentcontract.EffectiveRunConfig, error) {
	c := agentcontract.EffectiveRunConfig{Version: 1, Source: "current_host", Settings: NormalizedRunSettings(h.Settings)}
	if value, exists := run.State["effective_config"]; exists {
		raw, err := agentcontract.CanonicalJSON(value)
		if err != nil {
			return c, agentcontract.NewHostError("run_state_invalid")
		}
		var snapshot agentcontract.EffectiveRunConfig
		if err = json.Unmarshal(raw, &snapshot); err != nil || snapshot.Version != 1 || snapshot.Source != "run_snapshot" {
			return c, agentcontract.NewHostError("run_state_invalid")
		}
		c = snapshot
		c.Settings = NormalizedRunSettings(c.Settings)
	}
	if err := c.Settings.Validate(); err != nil {
		return c, agentcontract.NewHostError("run_state_invalid")
	}
	return c, nil
}

// Drivers validate this snapshot in restore before consuming execution limits.
func (h *AgentHost) runSettings(run agentcontract.StoredRun) agentcontract.HostSettings {
	c, _ := h.effectiveRunConfig(run) //nolint:errcheck // Drivers validate effective_config in restore before consuming these limits.
	return c.Settings
}
