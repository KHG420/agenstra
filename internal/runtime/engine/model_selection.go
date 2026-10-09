package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

// ModelConfiguration contains connection references, never resolved API keys.
// Empty purpose selections inherit DefaultProfile.
type ModelConfiguration struct {
	DefaultProfile          string                  `json:"default_profile"`
	DecisionProfile         string                  `json:"decision_profile,omitempty"`
	MemoryExtractionProfile string                  `json:"memory_extraction_profile,omitempty"`
	Profiles                map[string]ModelProfile `json:"profiles"`
}

// ModelProfile configures one model adapter using connection references rather than resolved secrets.
type ModelProfile struct {
	APIType               string       `json:"api_type"`
	DecisionOutputMode    string       `json:"decision_output_mode,omitempty"`
	Model                 string       `json:"model"`
	BaseURL               string       `json:"base_url,omitempty"`
	BaseURLEnv            string       `json:"base_url_env,omitempty"`
	APIKeyRef             string       `json:"api_key_ref"`
	Thinking              string       `json:"thinking,omitempty"`
	ReasoningEffort       string       `json:"reasoning_effort,omitempty"`
	Temperature           *float64     `json:"temperature,omitempty"`
	MaxOutputTokens       int          `json:"max_output_tokens,omitempty"`
	TokenLimitField       string       `json:"token_limit_field,omitempty"`
	ContextWindowTokens   int64        `json:"context_window_tokens,omitempty"`
	MaxInputTokens        int64        `json:"max_input_tokens,omitempty"`
	ProtocolReserveTokens int64        `json:"protocol_reserve_tokens,omitempty"`
	TimeoutSeconds        float64      `json:"timeout_seconds,omitempty"`
	MaxAttempts           int          `json:"max_attempts,omitempty"`
	Prices                *ModelPrices `json:"prices,omitempty"`
}

// ModelPrices holds optional per-million-token prices used only for cost estimates.
type ModelPrices struct {
	InputPerMillion       float64  `json:"input_per_million"`
	OutputPerMillion      float64  `json:"output_per_million"`
	CachedInputPerMillion *float64 `json:"cached_input_per_million,omitempty"`
}

// ModelSelectionSnapshot binds a configuration to a management revision.
type ModelSelectionSnapshot struct {
	Revision int                `json:"revision"`
	Config   ModelConfiguration `json:"config"`
}

func validateModelParameters(api, thinking, effort string, temperature *float64) error {
	if temperature != nil && (math.IsNaN(*temperature) || math.IsInf(*temperature, 0) || *temperature < 0 || *temperature > 2) {
		return registryError("model_parameters_invalid")
	}
	switch api {
	case "", "compatible_chat":
		if thinking != "" || effort != "" {
			return registryError("model_parameters_unsupported")
		}
	case "openai_chat":
		if thinking != "" || effort != "" && !containsString([]string{"none", "minimal", "low", "medium", "high", "xhigh"}, effort) {
			return registryError("model_parameters_unsupported")
		}
		if temperature != nil && effort != "" && effort != "none" {
			return registryError("model_parameters_unsupported")
		}
	case "deepseek_chat":
		if thinking != "" && thinking != "enabled" && thinking != "disabled" || effort != "" && !containsString([]string{"low", "high", "max"}, effort) {
			return registryError("model_parameters_unsupported")
		}
		if thinking == "disabled" && effort != "" || temperature != nil && thinking != "disabled" {
			return registryError("model_parameters_unsupported")
		}
	default:
		return registryError("model_api_type_unsupported")
	}
	return nil
}

func validModelURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func (c ModelConfiguration) profileID(purpose string) string {
	id := c.DefaultProfile
	if purpose == "decision" && c.DecisionProfile != "" {
		id = c.DecisionProfile
	}
	if purpose == "memory_extraction" && c.MemoryExtractionProfile != "" {
		id = c.MemoryExtractionProfile
	}
	return id
}

// Validate checks profiles, purpose selections and supported adapter parameters.
func (c ModelConfiguration) Validate() error {
	if len(c.Profiles) < 1 || len(c.Profiles) > 32 {
		return registryError("model_configuration_invalid")
	}
	for _, id := range []string{c.DefaultProfile, c.profileID("decision"), c.profileID("memory_extraction")} {
		if _, exists := c.Profiles[id]; !exists {
			return registryError("model_selection_invalid")
		}
	}
	for id, p := range c.Profiles {
		ref := strings.TrimPrefix(p.APIKeyRef, "secret:")
		if !registryID.MatchString(id) || len(p.Model) < 1 || len(p.Model) > 128 || strings.TrimSpace(p.Model) != p.Model || !deploymentEnvName.MatchString(ref) {
			return registryError("model_configuration_invalid")
		}
		if (p.BaseURL == "") == (p.BaseURLEnv == "") || p.BaseURL != "" && !validModelURL(p.BaseURL) || p.BaseURLEnv != "" && !deploymentEnvName.MatchString(p.BaseURLEnv) {
			return registryError("model_endpoint_invalid")
		}
		if err := validateModelParameters(p.APIType, p.Thinking, p.ReasoningEffort, p.Temperature); err != nil {
			return err
		}
		if !validDecisionOutputMode(p.DecisionOutputMode) {
			return registryError("model_parameters_invalid")
		}
		if p.TokenLimitField != "" && p.TokenLimitField != "max_tokens" && p.TokenLimitField != "max_completion_tokens" || p.MaxOutputTokens < 0 || p.MaxOutputTokens > 1000000 || p.ContextWindowTokens < 0 || p.ContextWindowTokens > 100000000 || p.MaxInputTokens < 0 || p.MaxInputTokens > 100000000 || p.ProtocolReserveTokens < 0 || p.ProtocolReserveTokens > 1000000 || p.MaxAttempts < 0 || p.MaxAttempts > 10 || math.IsNaN(p.TimeoutSeconds) || math.IsInf(p.TimeoutSeconds, 0) || p.TimeoutSeconds < 0 || p.TimeoutSeconds > 600 {
			return registryError("model_parameters_invalid")
		}
		if p.Prices != nil {
			values := []float64{p.Prices.InputPerMillion, p.Prices.OutputPerMillion}
			if p.Prices.CachedInputPerMillion != nil {
				values = append(values, *p.Prices.CachedInputPerMillion)
			}
			for _, v := range values {
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
					return registryError("model_prices_invalid")
				}
			}
		}
	}
	return nil
}

// ModelManager owns deployment selection. Runs retain a connection-reference
// snapshot, so editing the catalog cannot reroute an in-progress task.
type ModelManager struct {
	mu         sync.RWMutex
	deployment *Deployment
	selection  ModelSelectionSnapshot
}

// NewModel uses the catalog when configured and retains the existing three
// AGENT_MODEL* environment variables as the initial single-profile selection.
func (d *Deployment) NewModel() (*ModelManager, error) {
	c := d.Config.Models
	if c == nil {
		c = &ModelConfiguration{DefaultProfile: "default", Profiles: map[string]ModelProfile{"default": {
			APIType: "compatible_chat", Model: d.Environment["AGENT_MODEL"], BaseURLEnv: "AGENT_MODEL_BASE_URL", APIKeyRef: "AGENT_MODEL_API_KEY",
			MaxOutputTokens: d.Config.Settings.MaxModelOutputTokens, TokenLimitField: d.Config.Settings.ModelTokenLimitField,
		}}}
	}
	snapshot := ModelSelectionSnapshot{Config: *c}
	if d.Registry != nil {
		if err := d.Registry.Initialize(); err != nil {
			return nil, err
		}
		var payload string
		err := d.Registry.db.QueryRow(`SELECT revision,config_json FROM model_configuration WHERE id=1`).Scan(&snapshot.Revision, &payload)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == nil {
			if err = jsonvalue.DecodeStrict([]byte(payload), &snapshot.Config); err != nil {
				return nil, registryError("model_configuration_invalid")
			}
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
func (m *ModelManager) Snapshot() ModelSelectionSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	raw, err := json.Marshal(m.selection)
	if err != nil {
		return ModelSelectionSnapshot{}
	}
	var snapshot ModelSelectionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return ModelSelectionSnapshot{}
	}
	return snapshot
}

// Configure validates and persists a catalog edit under its expected revision.
// The manager retains its own configuration so caller mutations cannot reroute runs.
func (m *ModelManager) Configure(config ModelConfiguration, expectedRevision int) (result ModelSelectionSnapshot, resultErr error) {
	if _, err := m.selectedModel(config); err != nil {
		return ModelSelectionSnapshot{}, err
	}
	raw, err := CanonicalJSON(config)
	if err != nil {
		return ModelSelectionSnapshot{}, registryError("model_configuration_invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if expectedRevision != m.selection.Revision {
		return ModelSelectionSnapshot{}, registryError("model_revision_conflict")
	}
	r := m.deployment.Registry
	if r == nil {
		return ModelSelectionSnapshot{}, registryError("model_management_unavailable")
	}
	tx, err := r.db.Begin()
	if err != nil {
		return ModelSelectionSnapshot{}, err
	}
	defer func() {
		// A committed transaction is already closed; other rollback failures remain visible.
		if rollbackErr := tx.Rollback(); !errors.Is(rollbackErr, sql.ErrTxDone) {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()
	var revision int
	err = tx.QueryRow(`SELECT revision FROM model_configuration WHERE id=1`).Scan(&revision)
	if err != nil && err != sql.ErrNoRows {
		return ModelSelectionSnapshot{}, err
	}
	if revision != expectedRevision {
		return ModelSelectionSnapshot{}, registryError("model_revision_conflict")
	}
	if _, err = tx.Exec(`INSERT INTO model_configuration(id,revision,config_json) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,config_json=excluded.config_json`, revision+1, string(raw)); err != nil {
		return ModelSelectionSnapshot{}, err
	}
	if err = registryAudit(tx, "models_configured", "", JSON{"revision": revision + 1, "default_profile": config.DefaultProfile, "decision_profile": config.profileID("decision"), "memory_extraction_profile": config.profileID("memory_extraction")}); err != nil {
		return ModelSelectionSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return ModelSelectionSnapshot{}, err
	}
	// Own the decoded configuration; caller maps and pointers cannot mutate it.
	var owned ModelConfiguration
	if err := json.Unmarshal(raw, &owned); err != nil {
		return ModelSelectionSnapshot{}, err
	}
	m.selection = ModelSelectionSnapshot{Revision: revision + 1, Config: owned}
	return ModelSelectionSnapshot{Revision: revision + 1, Config: config}, nil
}

func (m *ModelManager) profile(id string, p ModelProfile) (*HTTPJSONDecisionModel, error) {
	base := p.BaseURL
	if base == "" {
		base = m.deployment.Environment[p.BaseURLEnv]
	}
	if !validModelURL(base) {
		return nil, registryError("model_endpoint_invalid")
	}
	key, err := m.deployment.Secret(p.APIKeyRef)
	if err != nil {
		return nil, registryError("model_credentials_unavailable")
	}
	timeout := p.TimeoutSeconds
	if timeout == 0 {
		timeout = normalizedRunSettings(m.deployment.Config.Settings).ModelTimeoutSeconds
	}
	model, err := NewHTTPJSONDecisionModel(p.Model, base, key, time.Duration(timeout*float64(time.Second)), nil)
	if err != nil {
		return nil, registryError("model_configuration_invalid")
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
	decision, memory *HTTPJSONDecisionModel
}

func (m *ModelManager) selectedModel(c ModelConfiguration) (*selectedModels, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	d, err := m.profile(c.profileID("decision"), c.Profiles[c.profileID("decision")])
	if err != nil {
		return nil, err
	}
	mem, err := m.profile(c.profileID("memory_extraction"), c.Profiles[c.profileID("memory_extraction")])
	if err != nil {
		return nil, err
	}
	return &selectedModels{decision: d, memory: mem}, nil
}

func (s *selectedModels) Decide(ctx context.Context, p ContextPacket, prompt string) (Decision, error) {
	return s.decision.Decide(ctx, p, prompt)
}

// ModelInfo reports the selected decision model capacity without making a request.
func (s *selectedModels) ModelInfo() ModelInfo { return s.decision.ModelInfo() }

// MeasureInput measures the selected decision model payload without executing a decision.
func (s *selectedModels) MeasureInput(p ContextPacket, prompt string) (InputMeasurement, error) {
	return s.decision.MeasureInput(p, prompt)
}

// ExtractMemories validates extracted preferences from the supplied input.
func (s *selectedModels) ExtractMemories(ctx context.Context, r MemoryExtractionRequest) ([]MemoryProposal, error) {
	return s.memory.ExtractMemories(ctx, r)
}

// ExtractMemoriesMeasured retains extraction request metrics on success and failure.
func (s *selectedModels) ExtractMemoriesMeasured(ctx context.Context, r MemoryExtractionRequest) ([]MemoryProposal, ModelCallMetrics, error) {
	return s.memory.ExtractMemoriesMeasured(ctx, r)
}

// Decide uses the current selected decision profile; durable runs use their pinned selection.
func (m *ModelManager) Decide(ctx context.Context, p ContextPacket, prompt string) (Decision, error) {
	s, err := m.selectedModel(m.Snapshot().Config)
	if err != nil {
		return Decision{}, err
	}
	return s.Decide(ctx, p, prompt)
}

// ModelInfo reports the current decision profile's configured capacity.
func (m *ModelManager) ModelInfo() ModelInfo {
	s, err := m.selectedModel(m.Snapshot().Config)
	if err != nil {
		return ModelInfo{}
	}
	return s.ModelInfo()
}

// MeasureInput measures the current decision profile's request payload.
func (m *ModelManager) MeasureInput(p ContextPacket, prompt string) (InputMeasurement, error) {
	s, err := m.selectedModel(m.Snapshot().Config)
	if err != nil {
		return InputMeasurement{}, err
	}
	return s.MeasureInput(p, prompt)
}

// ExtractMemories uses the current memory profile and validates proposals.
func (m *ModelManager) ExtractMemories(ctx context.Context, r MemoryExtractionRequest) ([]MemoryProposal, error) {
	p, _, err := m.ExtractMemoriesMeasured(ctx, r)
	return p, err
}

// ExtractMemoriesMeasured also returns request metrics for the memory profile.
func (m *ModelManager) ExtractMemoriesMeasured(ctx context.Context, r MemoryExtractionRequest) ([]MemoryProposal, ModelCallMetrics, error) {
	s, err := m.selectedModel(m.Snapshot().Config)
	if err != nil {
		return nil, ModelCallMetrics{}, err
	}
	return s.ExtractMemoriesMeasured(ctx, r)
}

// Close is a no-op because model adapters have no owned persistent connection.
func (m *ModelManager) Close() error { return nil }

func (h *AgentHost) modelForRun(run StoredRun) (DecisionModel, error) {
	manager, ok := h.Model.(*ModelManager)
	if !ok {
		return h.Model, nil
	}
	c, err := h.effectiveRunConfig(run)
	if err != nil {
		return nil, err
	}
	if c.ModelSelection == nil {
		return manager.selectedModel(manager.Snapshot().Config)
	}
	return manager.selectedModel(c.ModelSelection.Config)
}

// ModelCheckResult records the outcome and usage of one synthetic profile probe.
type ModelCheckResult struct {
	Profile   string           `json:"profile"`
	Purpose   string           `json:"purpose"`
	Passed    bool             `json:"passed"`
	ErrorCode string           `json:"error_code,omitempty"`
	Metrics   ModelCallMetrics `json:"metrics"`
}

// Check performs one synthetic structured request, never a business capability.
// A successful probe verifies that sample, not general model quality.
func (m *ModelManager) Check(ctx context.Context, id, purpose string, config *ModelConfiguration) (ModelCheckResult, error) {
	c := m.Snapshot().Config
	if config != nil {
		c = *config
	}
	if err := c.Validate(); err != nil {
		return ModelCheckResult{}, err
	}
	p, ok := c.Profiles[id]
	if !ok || purpose != "decision" && purpose != "memory_extraction" {
		return ModelCheckResult{}, registryError("model_selection_invalid")
	}
	model, err := m.profile(id, p)
	if err != nil {
		return ModelCheckResult{}, err
	}
	// A check makes exactly one transport attempt and does not consume run budget.
	model.MaxAttempts = 1
	result := ModelCheckResult{Profile: id, Purpose: purpose}
	if purpose == "memory_extraction" {
		_, result.Metrics, err = model.ExtractMemoriesMeasured(ctx, MemoryExtractionRequest{Text: "Remember: all my reports should use Chinese."})
	} else {
		var d Decision
		d, err = model.Decide(ctx, ContextPacket{}, `Return JSON only. This is a synthetic protocol check, with no business tools. Return exactly {"kind":"final","answer_markdown":"ok","fact_ids":[]}.`)
		if d.ModelCall != nil {
			result.Metrics = *d.ModelCall
		}
		if err == nil && d.Kind != "final" {
			err = ModelDecisionError{"model_decision_invalid"}
			result.Metrics.FormatError = "model_decision_schema_invalid"
		}
	}
	result.Metrics.Purpose = purpose
	result.Passed = err == nil
	if err != nil {
		result.ErrorCode = modelErrorCode(err)
		if result.Metrics.FormatError != "" {
			result.ErrorCode = result.Metrics.FormatError
		}
	}
	return result, nil
}
