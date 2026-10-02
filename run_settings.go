package agenstra

import "encoding/json"

// EffectiveRunConfig records execution limits, not credentials or authorizations.
// Lease renewal and host-wide run slots remain live host operations.
type EffectiveRunConfig struct {
	Model    ModelInfo    `json:"model"`
	Version  int          `json:"version"`
	Source   string       `json:"source"`
	Settings HostSettings `json:"settings"`
}

func normalizedRunSettings(s HostSettings) HostSettings {
	if s == (HostSettings{}) {
		s = DefaultHostSettings()
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

func (h *AgentHost) effectiveRunConfig(run StoredRun) (EffectiveRunConfig, error) {
	c := EffectiveRunConfig{Version: 1, Source: "current_host", Settings: normalizedRunSettings(h.Settings)}
	if value, exists := run.State["effective_config"]; exists {
		raw, err := CanonicalJSON(value)
		if err != nil {
			return c, hostError("run_state_invalid")
		}
		var snapshot EffectiveRunConfig
		if err = json.Unmarshal(raw, &snapshot); err != nil || snapshot.Version != 1 || snapshot.Source != "run_snapshot" {
			return c, hostError("run_state_invalid")
		}
		c = snapshot
		c.Settings = normalizedRunSettings(c.Settings)
	}
	if err := c.Settings.Validate(); err != nil {
		return c, hostError("run_state_invalid")
	}
	return c, nil
}

// Drivers validate this snapshot in restore before consuming execution limits.
func (h *AgentHost) runSettings(run StoredRun) HostSettings {
	c, _ := h.effectiveRunConfig(run)
	return c.Settings
}
