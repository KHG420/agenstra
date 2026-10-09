package service

import (
	"context"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type contextBudgetModel func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error)

func (m contextBudgetModel) Decide(ctx context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
	return m(ctx, packet, prompt)
}
