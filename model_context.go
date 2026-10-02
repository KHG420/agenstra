package agenstra

import (
	"errors"
)

// ModelInfoProvider is optional. Unknown capacities stay nil; no model-name guesses.
type ModelInfoProvider interface{ ModelInfo() ModelInfo }
type ModelInfo struct {
	Name                string `json:"name,omitempty"`
	ContextWindowTokens *int64 `json:"context_window_tokens"`
	MaxInputTokens      *int64 `json:"max_input_tokens"`
	MaxOutputTokens     *int64 `json:"max_output_tokens"`
}
type InputMeasurement struct {
	Tokens int64
	Source string
}
type ModelInputMeasurer interface {
	MeasureInput(ContextPacket, string) (InputMeasurement, error)
}

func knownTokens(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}
func modelInfo(model DecisionModel) ModelInfo {
	if m, ok := model.(ModelInfoProvider); ok {
		return m.ModelInfo()
	}
	return ModelInfo{}
}
func (m *HTTPJSONDecisionModel) ModelInfo() ModelInfo {
	return ModelInfo{Name: m.Model, ContextWindowTokens: knownTokens(m.ContextWindowTokens), MaxInputTokens: knownTokens(m.MaxInputTokens), MaxOutputTokens: knownTokens(int64(m.MaxOutputTokens))}
}

func (m *HTTPJSONDecisionModel) MeasureInput(packet ContextPacket, prompt string) (InputMeasurement, error) {
	input, err := CanonicalJSON(packet)
	if err != nil {
		return InputMeasurement{}, err
	}
	model := *m
	if packet.MaxModelOutputTokens > 0 && (model.MaxOutputTokens <= 0 || packet.MaxModelOutputTokens < model.MaxOutputTokens) {
		model.MaxOutputTokens = packet.MaxModelOutputTokens
	}
	payload, err := model.requestPayload(input, prompt)
	if err != nil {
		return InputMeasurement{}, err
	}
	raw, err := CanonicalJSON(payload)
	if err != nil {
		return InputMeasurement{}, err
	}
	return model.measurePayload(raw)
}
func (m *HTTPJSONDecisionModel) measurePayload(raw []byte) (InputMeasurement, error) {
	if m.CountInputTokens != nil {
		n, err := m.CountInputTokens(m.Model, raw)
		if err != nil || n < 0 {
			return InputMeasurement{}, errors.New("model_context_measurement_failed")
		}
		return InputMeasurement{n, "tokenizer"}, nil
	}
	return InputMeasurement{int64(len(raw) + 128), "utf8_bytes_estimate"}, nil
}
func (m *HTTPJSONDecisionModel) requestPayload(input []byte, prompt string) (JSON, error) {
	payload := JSON{"model": m.Model, "response_format": JSON{"type": "json_object"}, "messages": []any{JSON{"role": "system", "content": prompt}, JSON{"role": "user", "content": string(input)}}}
	if m.MaxOutputTokens > 0 {
		field := m.TokenLimitField
		if field == "" {
			field = "max_tokens"
		}
		if field != "max_tokens" && field != "max_completion_tokens" {
			return nil, ModelDecisionError{"model_token_limit_invalid"}
		}
		payload[field] = m.MaxOutputTokens
	}
	return payload, nil
}

func (r *AgentRuntime) tokenProjection(state *RuntimeState, packet ContextPacket, prompt string) (ContextPacket, *int64, *int64, *int64, InputMeasurement, error) {
	info := modelInfo(r.Model)
	window := r.ModelContextWindowTokens
	if info.ContextWindowTokens != nil && (window == 0 || *info.ContextWindowTokens < window) {
		window = *info.ContextWindowTokens
	}
	limit := r.MaxModelInputTokens
	if info.MaxInputTokens != nil && (limit == 0 || *info.MaxInputTokens < limit) {
		limit = *info.MaxInputTokens
	}
	reserve := int64(r.ModelOutputReserveTokens)
	if reserve == 0 {
		reserve = int64(r.MaxModelOutputTokens)
		if info.MaxOutputTokens != nil && (reserve == 0 || *info.MaxOutputTokens < reserve) {
			reserve = *info.MaxOutputTokens
		}
	}
	if window > 0 {
		if reserve <= 0 {
			return packet, knownTokens(window), nil, nil, InputMeasurement{}, errors.New("model_output_reserve_required")
		}
		available := window - reserve - r.ModelProtocolReserveTokens
		if available <= 0 {
			return packet, knownTokens(window), nil, knownTokens(reserve), InputMeasurement{}, errors.New("context_too_large")
		}
		if limit == 0 || available < limit {
			limit = available
		}
	}
	if reserve > 0 && (packet.MaxModelOutputTokens <= 0 || reserve < int64(packet.MaxModelOutputTokens)) {
		packet.MaxModelOutputTokens = int(reserve)
	}
	measure := func(p ContextPacket) (InputMeasurement, error) {
		if model, ok := r.Model.(ModelInputMeasurer); ok {
			return model.MeasureInput(p, prompt)
		}
		raw, _ := CanonicalJSON(p)
		return InputMeasurement{int64(len(raw) + len(prompt) + 128), "utf8_bytes_estimate"}, nil
	}
	if limit > 0 {
		packet.MaxModelInputTokens = limit
	}
	m, err := measure(packet)
	for i := 0; err == nil && limit > 0 && m.Tokens > limit && i < 24; i++ {
		before := contextCharacters(packet)
		allowance := int(float64(before)*float64(limit)/float64(m.Tokens)) - 16
		next := budgetContext(packet, state, max(0, allowance))
		packet = next
		m, err = measure(packet)
		if contextCharacters(next) >= before {
			break
		}
	}
	return packet, knownTokens(window), knownTokens(limit), knownTokens(reserve), m, err
}
