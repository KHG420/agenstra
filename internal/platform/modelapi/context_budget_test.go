package modelapi

import (
	"testing"
	"time"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

type contextBudgetProvider struct {
	coreTestProvider
	skills map[string]agentcontract.Skill
}

func (p *contextBudgetProvider) Skills() map[string]agentcontract.Skill { return p.skills }

func contextBudgetFact(value agentcontract.JSON) agentcontract.Fact {
	return agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "record.read", SourceVersion: "1", Value: value, Quality: "provider_reported", ObservedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), ReferenceScope: "durable"}
}

func contextBudgetRuntime(t *testing.T, budget int) (*reactcore.AgentRuntime, *agentcontract.RuntimeState, *contextBudgetProvider) {
	t.Helper()
	provider := &contextBudgetProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, skills: map[string]agentcontract.Skill{}}

	// Preserve the packet allowance while counting the runtime's fixed guidance.
	runtime := &reactcore.AgentRuntime{Provider: provider, Grants: map[string]bool{}}
	runtime.MaxContextCharacters = budget + utf8.RuneCountInString(runtime.SystemPrompt())
	state, err := runtime.NewState("Read records", "")
	if err != nil {
		t.Fatal(err)
	}
	return runtime, state, provider
}
