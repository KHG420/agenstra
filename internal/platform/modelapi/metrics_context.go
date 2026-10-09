package modelapi

import (
	"context"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type modelMetricsKey struct{}

type modelTokenBudgetKey struct{}

func packetTokenBudget(ctx context.Context) int64 {
	remaining, _ := ctx.Value(modelTokenBudgetKey{}).(int64)
	return remaining
}

func modelMetrics(ctx context.Context) *agentcontract.ModelCallMetrics {
	m, _ := ctx.Value(modelMetricsKey{}).(*agentcontract.ModelCallMetrics)
	return m
}
