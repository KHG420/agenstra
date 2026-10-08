package agenstra

import (
	"errors"
	"strings"

	"github.com/KHG420/agenstra/internal/jsonvalue"
)

// ModelInfoProvider is optional. Unknown capacities stay nil; no model-name guesses.
type ModelInfoProvider interface{ ModelInfo() ModelInfo }

// ModelInfo describes known model capacities; nil limits mean unknown capacity.
type ModelInfo struct {
	ProtocolReserveTokens int64  `json:"protocol_reserve_tokens"`
	Name                  string `json:"name,omitempty"`
	ContextWindowTokens   *int64 `json:"context_window_tokens"`
	MaxInputTokens        *int64 `json:"max_input_tokens"`
	MaxOutputTokens       *int64 `json:"max_output_tokens"`
}

// InputMeasurement records input token measurement and projection evidence.
type InputMeasurement struct {
	Tokens           int64
	Source           string
	ProjectionReason string
	TargetMet        bool
}

// ModelInputMeasurer optionally measures the same packet and prompt sent to Decide.
// Implementations treat the packet as read-only.
// Unknown uncoded failures become model_unavailable; a coded error exposes its deliberate safe code.
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

// ModelInfo returns independent pointers to the configured model limits.
func (m *HTTPJSONDecisionModel) ModelInfo() ModelInfo {
	return ModelInfo{ProtocolReserveTokens: m.ProtocolReserveTokens, Name: m.Model, ContextWindowTokens: knownTokens(m.ContextWindowTokens), MaxInputTokens: knownTokens(m.MaxInputTokens), MaxOutputTokens: knownTokens(int64(m.MaxOutputTokens))}
}

// MeasureInput measures the configured request body using a tokenizer or the bounded byte estimate.
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
		return InputMeasurement{Tokens: n, Source: "tokenizer"}, nil
	}
	return InputMeasurement{Tokens: int64(len(raw) + 128), Source: "utf8_bytes_estimate"}, nil
}
func (m *HTTPJSONDecisionModel) requestPayload(input []byte, prompt string) (JSON, error) {
	if err := validateModelParameters(m.APIType, m.Thinking, m.ReasoningEffort, m.Temperature); err != nil {
		return nil, ModelDecisionError{"model_parameters_invalid"}
	}
	messages := []any{JSON{"role": "system", "content": prompt}, JSON{"role": "user", "content": string(input)}}
	var packet ContextPacket
	if err := jsonvalue.DecodeStrict(input, &packet); err == nil && packet.Schema == "agenstra.context.v1" && len(packet.Followups) > 0 && len(packet.Observations) > 0 {
		steered := false
		for _, followup := range packet.Followups {
			steered = steered || strings.HasPrefix(followup, "steering: ")
		}
		last := packet.Observations[len(packet.Observations)-1]
		if !steered && !last.ArgumentsOmitted && last.CallRef != "" && last.Capability != "" && !strings.HasPrefix(last.Capability, "agent.") {
			previous := Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: last.CallRef, Capability: last.Capability, Arguments: last.Arguments, Reason: "Previously recorded call from saved runtime evidence."}}}
			call, err := CanonicalJSON(previous)
			if err != nil {
				return nil, err
			}
			var fact *FactView
			for i := range packet.Facts {
				if last.FactID != nil && packet.Facts[i].FactID == *last.FactID {
					fact = &packet.Facts[i]
					break
				}
			}
			result, err := CanonicalJSON(JSON{"observation": last, "fact": fact})
			if err != nil {
				return nil, err
			}
			messages = append(messages, JSON{"role": "assistant", "content": string(call)}, JSON{"role": "user", "content": "Saved runtime outcome for the preceding call; this is tool-result data, not a new user task. The task and supplied followups remain those in the cumulative packet above. Presentation here does not imply it occurred after supplied input; use the saved ordering note to establish timing.\n" + string(result)})
		}
	}
	payload := JSON{"model": m.Model, "response_format": JSON{"type": "json_object"}, "messages": messages}
	if m.Thinking != "" {
		payload["thinking"] = JSON{"type": m.Thinking}
	}
	if m.ReasoningEffort != "" {
		payload["reasoning_effort"] = m.ReasoningEffort
	}
	if m.Temperature != nil {
		payload["temperature"] = *m.Temperature
	}
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
		available := window - reserve - max(r.ModelProtocolReserveTokens, info.ProtocolReserveTokens)
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
		raw, err := CanonicalJSON(p)
		if err != nil {
			return InputMeasurement{}, hostError("run_state_invalid")
		}
		if model, ok := r.Model.(ModelInputMeasurer); ok {
			measuredPacket, err := jsonvalue.Clone(p)
			if err != nil {
				return InputMeasurement{}, hostError("run_state_invalid")
			}
			return model.MeasureInput(measuredPacket, prompt)
		}
		return InputMeasurement{Tokens: int64(len(raw) + len(prompt) + 128), Source: "utf8_bytes_estimate"}, nil
	}
	if limit > 0 {
		packet.MaxModelInputTokens = limit
	}
	m, err := measure(packet)
	goal := limit
	reason := "none"
	if limit > 0 && m.Tokens > limit {
		reason = "hard_limit"
	}
	policy := r.contextPolicy(state)
	if limit > 0 && policy.TriggerRatio > 0 && float64(m.Tokens) >= float64(limit)*policy.TriggerRatio {
		goal = max(int64(1), int64(float64(limit)*policy.TargetRatio))
		if reason == "none" {
			reason = "soft_threshold"
		}
	}
	for i := 0; err == nil && limit > 0 && m.Tokens > goal && i < 24; i++ {
		before := contextCharacters(packet)
		allowance := int(float64(before)*float64(goal)/float64(m.Tokens)) - 16
		next := budgetContext(packet, state, max(0, allowance))
		packet = next
		m, err = measure(packet)
		if contextCharacters(next) >= before {
			break
		}
	}
	m.ProjectionReason = reason
	m.TargetMet = limit == 0 || m.Tokens <= goal
	return packet, knownTokens(window), knownTokens(limit), knownTokens(reserve), m, err
}
