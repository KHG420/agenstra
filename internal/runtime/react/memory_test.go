package react

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestMemoryContextSharesBudgetAndPreservesRequiredConventions(t *testing.T) {
	runtime, state, provider := contextBudgetRuntime(t, 2200)
	for i := 0; i < 8; i++ {
		runtime.Memories = append(runtime.Memories, agentcontract.MemoryView{ID: agentcontract.NewID(), Key: fmt.Sprintf("preference.%d", i), Value: strings.Repeat("偏好", 180), Kind: "preference", Scope: "user", Revision: 1})
	}
	packet := runtime.Context(state)
	assertContextBudget(t, packet, runtime.SystemPrompt(), runtime.MaxContextCharacters)
	if len(packet.Memories) >= 8 || len(packet.ContextOmissions) == 0 {
		t.Fatal("memory not budgeted", packet.Memories)
	}
	second := budgetContext(packet, state, 1200-len(provider.SystemPrompt())-len(agentcontract.MemoryUsagePrompt))
	if len(second.Memories) > len(packet.Memories) {
		t.Fatal("defaults restored under increased pressure")
	}
	allOmitted := fmt.Sprintf("memories: %d defaults omitted", 8-len(second.Memories))
	if !slices.Contains(second.ContextOmissions, allOmitted) {
		t.Fatal("omission count lost across budgeting", second.ContextOmissions, allOmitted)
	}
	for i := range runtime.Memories {
		runtime.Memories[i].Kind = "convention"
	}
	model := &hostModel{}
	runtime.Model = model
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "context_too_large" || model.calls != 0 {
		t.Fatal("required convention discarded", state, model.calls)
	}
}
