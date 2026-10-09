package modelapi

import (
	"context"
	"sync"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type hostProvider struct {
	mu      sync.Mutex
	caps    map[string]agentcontract.CapabilityDescription
	calls   int
	hook    func(context.Context, string, map[string]any, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error)
	binding string
}

func (p *hostProvider) Capabilities() map[string]agentcontract.CapabilityDescription {
	if p.caps != nil {
		return p.caps
	}
	return map[string]agentcontract.CapabilityDescription{"records.get": {Name: "records.get", Version: "1", Description: "Look up record", InputSchema: agentcontract.JSON{"type": "object"}, Effect: "read", Replay: "safe", ReferenceScope: "durable", SkillsList: []string{}}}
}

func (p *hostProvider) Skills() map[string]agentcontract.Skill {
	return map[string]agentcontract.Skill{}
}

func (p *hostProvider) SystemPrompt() string { return "Use capabilities" }

func (p *hostProvider) BindingID() string { return p.binding }

func (p *hostProvider) Close() error { return nil }

func (p *hostProvider) Invoke(ctx context.Context, name string, args map[string]any, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.hook != nil {
		return p.hook(ctx, name, args, inv)
	}
	return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "R-1"}, ReferenceScope: "durable"}, nil
}
