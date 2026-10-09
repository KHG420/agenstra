package deployment

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/platform/modelapi"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
)

// ModelManager owns deployment selection. Runs retain a connection-reference
// snapshot, so editing the catalog cannot reroute an in-progress task.
type ModelManager struct {
	mu         sync.RWMutex
	deployment *Deployment
	selection  agentcontract.ModelSelectionSnapshot
}

// NewModel uses the catalog when configured and retains the existing three
// AGENT_MODEL* environment variables as the initial single-profile selection.
func (d *Deployment) NewModel() (*ModelManager, error) {
	c := d.Config.Models
	if c == nil {
		c = &agentcontract.ModelConfiguration{DefaultProfile: "default", Profiles: map[string]agentcontract.ModelProfile{"default": {
			APIType: "compatible_chat", Model: d.Environment["AGENT_MODEL"], BaseURLEnv: "AGENT_MODEL_BASE_URL", APIKeyRef: "AGENT_MODEL_API_KEY",
			MaxOutputTokens: d.Config.Settings.MaxModelOutputTokens, TokenLimitField: d.Config.Settings.ModelTokenLimitField,
		}}}
	}
	snapshot := agentcontract.ModelSelectionSnapshot{Config: *c}
	if d.Registry != nil {
		if err := d.Registry.Initialize(); err != nil {
			return nil, err
		}
		stored, exists, err := d.Registry.ModelSelection()
		if err != nil {
			return nil, err
		}
		if exists {
			snapshot = stored
		}
	}
	m := &ModelManager{deployment: d, selection: snapshot}
	if _, err := m.selectedModel(snapshot.Config); err != nil {
		return nil, err
	}
	m.selection = m.Snapshot()
	return m, nil
}

// Snapshot returns an independent copy of the selected configuration.
func (m *ModelManager) Snapshot() agentcontract.ModelSelectionSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	raw, err := json.Marshal(m.selection)
	if err != nil {
		return agentcontract.ModelSelectionSnapshot{}
	}
	var snapshot agentcontract.ModelSelectionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return agentcontract.ModelSelectionSnapshot{}
	}
	return snapshot
}

// Configure validates and persists a catalog edit under its expected revision.
// The manager retains its own configuration so caller mutations cannot reroute runs.
func (m *ModelManager) Configure(config agentcontract.ModelConfiguration, expectedRevision int) (agentcontract.ModelSelectionSnapshot, error) {
	if _, err := m.selectedModel(config); err != nil {
		return agentcontract.ModelSelectionSnapshot{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if expectedRevision != m.selection.Revision {
		return agentcontract.ModelSelectionSnapshot{}, agentcontract.NewRegistryError("model_revision_conflict")
	}
	r := m.deployment.Registry
	if r == nil {
		return agentcontract.ModelSelectionSnapshot{}, agentcontract.NewRegistryError("model_management_unavailable")
	}
	snapshot, err := r.SaveModelConfiguration(config, expectedRevision)
	if err != nil {
		return agentcontract.ModelSelectionSnapshot{}, err
	}
	m.selection = snapshot
	return agentcontract.ModelSelectionSnapshot{Revision: snapshot.Revision, Config: config}, nil
}

func (m *ModelManager) profile(id string, p agentcontract.ModelProfile) (*modelapi.HTTPJSONDecisionModel, error) {
	base := p.BaseURL
	if base == "" {
		base = m.deployment.Environment[p.BaseURLEnv]
	}
	if !agentcontract.ValidModelURL(base) {
		return nil, agentcontract.NewRegistryError("model_endpoint_invalid")
	}
	key, err := m.deployment.Secret(p.APIKeyRef)
	if err != nil {
		return nil, agentcontract.NewRegistryError("model_credentials_unavailable")
	}
	timeout := p.TimeoutSeconds
	if timeout == 0 {
		timeout = durablehost.NormalizedRunSettings(m.deployment.Config.Settings).ModelTimeoutSeconds
	}
	model, err := modelapi.NewHTTPJSONDecisionModel(p.Model, base, key, time.Duration(timeout*float64(time.Second)), nil)
	if err != nil {
		return nil, agentcontract.NewRegistryError("model_configuration_invalid")
	}
	model.Profile, model.APIType = id, p.APIType
	model.DecisionOutputMode = p.DecisionOutputMode
	model.Thinking, model.ReasoningEffort, model.Temperature = p.Thinking, p.ReasoningEffort, p.Temperature
	model.MaxOutputTokens, model.TokenLimitField = p.MaxOutputTokens, p.TokenLimitField
	if model.TokenLimitField == "" && p.APIType == "openai_chat" {
		model.TokenLimitField = "max_completion_tokens"
	}
	model.ContextWindowTokens, model.MaxInputTokens, model.ProtocolReserveTokens = p.ContextWindowTokens, p.MaxInputTokens, p.ProtocolReserveTokens
	model.MaxAttempts = p.MaxAttempts
	if p.Prices != nil {
		model.PricesConfigured = true
		model.InputPricePerMillion, model.OutputPricePerMillion = p.Prices.InputPerMillion, p.Prices.OutputPerMillion
		model.CachedInputPricePerMillion = p.Prices.CachedInputPerMillion
	}
	return model, nil
}

type selectedModels struct {
	decision, memory *modelapi.HTTPJSONDecisionModel
}

func (m *ModelManager) selectedModel(c agentcontract.ModelConfiguration) (*selectedModels, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	d, err := m.profile(c.ProfileID("decision"), c.Profiles[c.ProfileID("decision")])
	if err != nil {
		return nil, err
	}
	mem, err := m.profile(c.ProfileID("memory_extraction"), c.Profiles[c.ProfileID("memory_extraction")])
	if err != nil {
		return nil, err
	}
	return &selectedModels{decision: d, memory: mem}, nil
}

// SelectModel opens adapters for a frozen run configuration using trusted
// deployment connection references. It does not change the live selection.
func (m *ModelManager) SelectModel(c agentcontract.ModelConfiguration) (agentcontract.DecisionModel, error) {
	return m.selectedModel(c)
}

// Decide delegates to the decision adapter selected for this frozen configuration.
func (s *selectedModels) Decide(ctx context.Context, p agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
	return s.decision.Decide(ctx, p, prompt)
}

// ModelInfo reports the selected decision model capacity without making a request.
func (s *selectedModels) ModelInfo() agentcontract.ModelInfo { return s.decision.ModelInfo() }

// MeasureInput measures the selected decision model payload without executing a decision.
func (s *selectedModels) MeasureInput(p agentcontract.ContextPacket, prompt string) (agentcontract.InputMeasurement, error) {
	return s.decision.MeasureInput(p, prompt)
}

// ExtractMemories validates extracted preferences from the supplied input.
func (s *selectedModels) ExtractMemories(ctx context.Context, r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
	return s.memory.ExtractMemories(ctx, r)
}

// ExtractMemoriesMeasured retains extraction request metrics on success and failure.
func (s *selectedModels) ExtractMemoriesMeasured(ctx context.Context, r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, agentcontract.ModelCallMetrics, error) {
	return s.memory.ExtractMemoriesMeasured(ctx, r)
}

// Decide uses the current selected decision profile; durable runs use their pinned selection.
func (m *ModelManager) Decide(ctx context.Context, p agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
	s, err := m.selectedModel(m.Snapshot().Config)
	if err != nil {
		return agentcontract.Decision{}, err
	}
	return s.Decide(ctx, p, prompt)
}

// ModelInfo reports the current decision profile's configured capacity.
func (m *ModelManager) ModelInfo() agentcontract.ModelInfo {
	s, err := m.selectedModel(m.Snapshot().Config)
	if err != nil {
		return agentcontract.ModelInfo{}
	}
	return s.ModelInfo()
}

// MeasureInput measures the current decision profile's request payload.
func (m *ModelManager) MeasureInput(p agentcontract.ContextPacket, prompt string) (agentcontract.InputMeasurement, error) {
	s, err := m.selectedModel(m.Snapshot().Config)
	if err != nil {
		return agentcontract.InputMeasurement{}, err
	}
	return s.MeasureInput(p, prompt)
}

// ExtractMemories uses the current memory profile and validates proposals.
func (m *ModelManager) ExtractMemories(ctx context.Context, r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
	p, _, err := m.ExtractMemoriesMeasured(ctx, r)
	return p, err
}

// ExtractMemoriesMeasured also returns request metrics for the memory profile.
func (m *ModelManager) ExtractMemoriesMeasured(ctx context.Context, r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, agentcontract.ModelCallMetrics, error) {
	s, err := m.selectedModel(m.Snapshot().Config)
	if err != nil {
		return nil, agentcontract.ModelCallMetrics{}, err
	}
	return s.ExtractMemoriesMeasured(ctx, r)
}

// Close is a no-op because model adapters have no owned persistent connection.
func (m *ModelManager) Close() error { return nil }

// Check performs one synthetic structured request, never a business capability.
// A successful probe verifies that sample, not general model quality.
func (m *ModelManager) Check(ctx context.Context, id, purpose string, config *agentcontract.ModelConfiguration) (agentcontract.ModelCheckResult, error) {
	c := m.Snapshot().Config
	if config != nil {
		c = *config
	}
	if err := c.Validate(); err != nil {
		return agentcontract.ModelCheckResult{}, err
	}
	p, ok := c.Profiles[id]
	if !ok || purpose != "decision" && purpose != "memory_extraction" {
		return agentcontract.ModelCheckResult{}, agentcontract.NewRegistryError("model_selection_invalid")
	}
	model, err := m.profile(id, p)
	if err != nil {
		return agentcontract.ModelCheckResult{}, err
	}

	// A check makes exactly one transport attempt and does not consume run budget.
	model.MaxAttempts = 1
	result := agentcontract.ModelCheckResult{Profile: id, Purpose: purpose}
	if purpose == "memory_extraction" {
		_, result.Metrics, err = model.ExtractMemoriesMeasured(ctx, agentcontract.MemoryExtractionRequest{Text: "Remember: all my reports should use Chinese."})
	} else {
		var d agentcontract.Decision
		d, err = model.Decide(ctx, agentcontract.ContextPacket{}, `Return JSON only. This is a synthetic protocol check, with no business tools. Return exactly {"kind":"final","answer_markdown":"ok","fact_ids":[]}.`)
		if d.ModelCall != nil {
			result.Metrics = *d.ModelCall
		}
		if err == nil && d.Kind != "final" {
			err = agentcontract.ModelDecisionError{Kind: "model_decision_invalid"}
			result.Metrics.FormatError = "model_decision_schema_invalid"
		}
	}
	result.Metrics.Purpose = purpose
	result.Passed = err == nil
	if err != nil {
		result.ErrorCode = agentcontract.ModelErrorCode(err)
		if result.Metrics.FormatError != "" {
			result.ErrorCode = result.Metrics.FormatError
		}
	}
	return result, nil
}
