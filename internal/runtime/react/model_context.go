package react

import (
	"errors"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func (r *AgentRuntime) tokenProjection(state *agentcontract.RuntimeState, packet agentcontract.ContextPacket, prompt string) (agentcontract.ContextPacket, *int64, *int64, *int64, agentcontract.InputMeasurement, error) {
	info := agentcontract.DescribeModel(r.Model)
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
			return packet, agentcontract.KnownTokens(window), nil, nil, agentcontract.InputMeasurement{}, errors.New("model_output_reserve_required")
		}
		available := window - reserve - max(r.ModelProtocolReserveTokens, info.ProtocolReserveTokens)
		if available <= 0 {
			return packet, agentcontract.KnownTokens(window), nil, agentcontract.KnownTokens(reserve), agentcontract.InputMeasurement{}, errors.New("context_too_large")
		}
		if limit == 0 || available < limit {
			limit = available
		}
	}
	if reserve > 0 && (packet.MaxModelOutputTokens <= 0 || reserve < int64(packet.MaxModelOutputTokens)) {
		packet.MaxModelOutputTokens = int(reserve)
	}
	measure := func(p agentcontract.ContextPacket) (agentcontract.InputMeasurement, error) {
		raw, err := agentcontract.CanonicalJSON(p)
		if err != nil {
			return agentcontract.InputMeasurement{}, agentcontract.NewHostError("run_state_invalid")
		}
		if model, ok := r.Model.(agentcontract.ModelInputMeasurer); ok {
			measuredPacket, err := jsonvalue.Clone(p)
			if err != nil {
				return agentcontract.InputMeasurement{}, agentcontract.NewHostError("run_state_invalid")
			}
			return model.MeasureInput(measuredPacket, prompt)
		}
		return agentcontract.InputMeasurement{Tokens: int64(len(raw) + len(prompt) + 128), Source: "utf8_bytes_estimate"}, nil
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
	return packet, agentcontract.KnownTokens(window), agentcontract.KnownTokens(limit), agentcontract.KnownTokens(reserve), m, err
}
