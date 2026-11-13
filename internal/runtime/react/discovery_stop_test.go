package react

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestUnproductiveCapabilityDiscoveryCanAnswerBeforeStagnation(t *testing.T) {
	provider := &coreTestProvider{caps: searchTestCapabilities()}
	calls := 0
	runtime := &AgentRuntime{Provider: provider, Grants: map[string]bool{"alpha.read": true}, MaxContextCapabilities: 2,
		Model: decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
			calls++
			if calls <= 2 {
				if !slices.Contains(packet.RuntimeFeatures, "capability_search") {
					t.Fatal("initial discovery or one alternate query was unavailable")
				}
				return agentcontract.Decision{Kind: "search_capabilities", Query: fmt.Sprintf("unsupported-request-%d", calls)}, nil
			}
			if slices.Contains(packet.RuntimeFeatures, "capability_search") || !strings.Contains(prompt, capabilityDiscoveryStopPrompt) {
				t.Fatal("equivalent unsuccessful searches did not trigger bounded discovery")
			}
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "当前接入的功能没有提供这项操作。", FactIDs: []string{}}, nil
		})}
	state, err := runtime.NewState("Perform an unsupported request", "")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := runtime.Step(t.Context(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored agentcontract.RuntimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Step(t.Context(), &restored, nil); err != nil || restored.Status != "completed" || calls != 3 || provider.called != 0 {
		t.Fatalf("unsupported request did not produce a bounded reply: status=%s calls=%d provider=%d error=%v", restored.Status, calls, provider.called, err)
	}
}

func TestDiscoveryRetainsDifferentSearchesAndResetsAfterNewEvidence(t *testing.T) {
	provider := &coreTestProvider{caps: searchTestCapabilities()}
	runtime := &AgentRuntime{Provider: provider, Grants: map[string]bool{"alpha.read": true, "beta.read": true}, MaxContextCapabilities: 1}
	state, err := runtime.NewState("Read accounts", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Progress = &agentcontract.ProgressTracker{NoProgressRounds: 2}
	state.Decisions = []agentcontract.JSON{{"kind": "search_capabilities", "query": "alpha.read"}, {"kind": "search_capabilities", "query": "beta.read"}}
	if !slices.Contains(runtime.Context(state).RuntimeFeatures, "capability_search") {
		t.Fatal("different discovered capabilities were treated as a loop")
	}
	state.Decisions[1]["query"] = "alpha.read"
	if slices.Contains(runtime.Context(state).RuntimeFeatures, "capability_search") {
		t.Fatal("repeated candidates did not pause discovery")
	}
	state.Progress.NoProgressRounds = 0
	if !slices.Contains(runtime.Context(state).RuntimeFeatures, "capability_search") {
		t.Fatal("new evidence or user input did not restore discovery")
	}
	state.Progress.NoProgressRounds = 2
	state.Decisions = append(state.Decisions, agentcontract.JSON{"kind": "tool_batch"})
	if !slices.Contains(runtime.Context(state).RuntimeFeatures, "capability_search") {
		t.Fatal("a subsequent business operation did not start a new discovery phase")
	}
}

func TestRepeatedFailureFeedbackAllowsAnEarlyLimitationReply(t *testing.T) {
	calls := 0
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: searchTestCapabilities()}, Grants: map[string]bool{},
		Model: decisionModelFunc(func(_ context.Context, _ agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
			calls++
			if calls <= 2 {
				return agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: fmt.Sprintf("read-%d", calls), Capability: "alpha.read", Arguments: agentcontract.JSON{}, Reason: "Read the requested account"}}}, nil
			}
			if !strings.Contains(prompt, "Repeated failed call detected:") || !strings.Contains(prompt, "capability_not_granted") {
				t.Fatal("repeated permission failure was not explained before the hard stop")
			}
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "当前账号没有这项读取权限。", FactIDs: []string{}}, nil
		})}
	result, err := runtime.Run(t.Context(), "Read the account")
	if err != nil || result.Status != "completed" || calls != 3 {
		t.Fatalf("failed calls exhausted the run instead of producing a reply: status=%s code=%v calls=%d error=%v", result.Status, result.ErrorCode, calls, err)
	}
}
