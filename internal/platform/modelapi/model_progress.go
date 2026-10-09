package modelapi

import (
	"context"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func observeModelRequest(ctx context.Context, p agentcontract.ModelRequestProgress) error {
	return agentcontract.NotifyModelRequest(ctx, p)
}
