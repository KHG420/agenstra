package react

import (
	"math"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func (r *AgentRuntime) contextPolicy(state *agentcontract.RuntimeState) agentcontract.ContextPolicy {
	if state.ContextPolicy != nil {
		return *state.ContextPolicy
	}
	return r.ContextPolicy
}

func (r *AgentRuntime) characterProjection(state *agentcontract.RuntimeState, packet agentcontract.ContextPacket, prompt string) (agentcontract.ContextPacket, string, bool) {
	policy := r.contextPolicy(state)
	limit := r.MaxContextCharacters
	promptSize := utf8.RuneCountInString(prompt)
	size := contextCharacters(packet)
	if size <= math.MaxInt-promptSize {
		size += promptSize
	}
	goal := limit
	reason := "none"
	if size > limit {
		reason = "hard_limit"
	}
	info := agentcontract.DescribeModel(r.Model)
	tokensKnown := r.MaxModelInputTokens > 0 || r.ModelContextWindowTokens > 0 || info.MaxInputTokens != nil || info.ContextWindowTokens != nil
	if !tokensKnown && policy.TriggerRatio > 0 && float64(size) >= float64(limit)*policy.TriggerRatio {
		goal = int(float64(limit) * policy.TargetRatio)
		if reason == "none" {
			reason = "soft_threshold"
		}
	}
	projected := budgetContext(packet, state, goal-utf8.RuneCountInString(prompt))
	return projected, reason, contextCharacters(projected) <= goal-promptSize
}
