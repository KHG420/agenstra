package modelapi

import (
	"context"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type coreTestProvider struct {
	caps   map[string]agentcontract.CapabilityDescription
	called int
}

func (p *coreTestProvider) Capabilities() map[string]agentcontract.CapabilityDescription {
	return p.caps
}

func (p *coreTestProvider) Skills() map[string]agentcontract.Skill {
	return map[string]agentcontract.Skill{}
}

func (p *coreTestProvider) SystemPrompt() string { return "test" }

func (p *coreTestProvider) Invoke(_ context.Context, _ string, _ map[string]any, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
	p.called++
	return agentcontract.CapabilityResult{Data: agentcontract.JSON{"value": 42}}, nil
}

func (p *coreTestProvider) Close() error { return nil }
