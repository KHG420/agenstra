package react

import (
	"context"
	"sync"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type hostModel struct {
	mu        sync.Mutex
	decisions []agentcontract.Decision
	calls     int
	hook      func(agentcontract.ContextPacket)
}

func (m *hostModel) Decide(ctx context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hook != nil {
		m.hook(packet)
	}
	m.calls++
	if len(m.decisions) == 0 {
		ids := []string{}
		for _, fact := range packet.Facts {
			ids = append(ids, fact.FactID)
		}
		return agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Done", FactIDs: ids}, nil
	}
	d := m.decisions[0]
	m.decisions = m.decisions[1:]
	return d, nil
}

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

func callDecision(name string) agentcontract.Decision {
	return agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "lookup-1", Capability: name, Arguments: agentcontract.JSON{"id": "R-1"}, Reason: "Look up record"}}}
}
