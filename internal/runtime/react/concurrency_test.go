package react

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type parallelTestProvider struct{ *hostProvider }

func (*parallelTestProvider) ConcurrentInvocation(string) bool { return true }

func concurrentDecision() agentcontract.Decision {
	d := agentcontract.Decision{Kind: "tool_batch"}
	for i := 0; i < 4; i++ {
		d.Calls = append(d.Calls, agentcontract.ToolCall{CallRef: fmt.Sprintf("read-%d", i), Capability: "records.get", Arguments: agentcontract.JSON{"id": fmt.Sprint(i)}, Reason: "Read independent record"})
	}
	return d
}

func TestTransientRuntimeAlsoExecutesIndependentCallsConcurrently(t *testing.T) {
	var started atomic.Int32
	gate := make(chan struct{})
	p := &parallelTestProvider{&hostProvider{hook: func(ctx context.Context, _ string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		if started.Add(1) == 2 {
			close(gate)
		}
		select {
		case <-gate:
			return agentcontract.CapabilityResult{Data: args}, nil
		case <-ctx.Done():
			return agentcontract.CapabilityResult{}, ctx.Err()
		}
	}}}
	r := &AgentRuntime{Provider: p, Model: &hostModel{decisions: []agentcontract.Decision{concurrentDecision()}}, Grants: map[string]bool{"records.get": true}, MaxConcurrentTools: 2}
	result, err := r.Run(t.Context(), "Read four records")
	if err != nil || result.Status != "completed" || len(result.Facts) != 4 {
		t.Fatal(result, err)
	}
	for i, obs := range result.Observations {
		if obs.CallRef != fmt.Sprintf("read-%d", i) {
			t.Fatal(result.Observations)
		}
	}
}

func TestConcurrencyExcludesWritesApprovalsJobsAndUnsafeReplay(t *testing.T) {
	policy := agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{"records.get": true}}
	for _, variant := range []string{"write", "approval", "job", "replay", "provider", "limit"} {
		t.Run(variant, func(t *testing.T) {
			cap := agentcontract.CapabilityDescription{Name: "records.get", Effect: "read", Replay: "safe"}
			if variant == "write" {
				cap.Effect = "write"
			}
			if variant == "approval" {
				cap.ApprovalRequired = true
			}
			if variant == "job" {
				cap.Operation = &agentcontract.OperationBinding{}
			}
			if variant == "replay" {
				cap.Replay = "never"
			}
			base := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
			var provider agentcontract.CapabilityProvider = &parallelTestProvider{base}
			if variant == "provider" {
				provider = base
			}
			limit := 4
			if variant == "limit" {
				limit = 1
			}
			pending := []agentcontract.Invocation{{Call: agentcontract.ToolCall{Capability: cap.Name}, Status: "prepared"}, {Call: agentcontract.ToolCall{Capability: cap.Name}, Status: "prepared"}}
			if IndependentBatch(provider, pending, policy, limit) {
				t.Fatal("unsafe parallel batch")
			}
		})
	}
}
