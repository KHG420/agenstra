package react

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

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

func TestTransientParallelCancellationRetainsReturnedOutcomes(t *testing.T) {
	for _, sibling := range []string{"unknown", "failed", "succeeded"} {
		t.Run(sibling, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			var started atomic.Int32
			gate, firstReturned := make(chan struct{}), make(chan struct{})
			p := &parallelTestProvider{&hostProvider{hook: func(ctx context.Context, _ string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
				if started.Add(1) == 2 {
					close(gate)
				}
				select {
				case <-gate:
				case <-ctx.Done():
					return agentcontract.CapabilityResult{}, ctx.Err()
				}
				if args["id"] == "A" {
					close(firstReturned)
					return agentcontract.CapabilityResult{Data: args}, nil
				}
				select {
				case <-firstReturned:
				case <-ctx.Done():
					return agentcontract.CapabilityResult{}, ctx.Err()
				}
				cancel()
				switch sibling {
				case "failed":
					return agentcontract.CapabilityResult{ErrorCode: "business_failed"}, nil
				case "succeeded":
					return agentcontract.CapabilityResult{Data: args}, nil
				default:
					return agentcontract.CapabilityResult{}, context.Canceled
				}
			}}}
			model := &hostModel{decisions: []agentcontract.Decision{{Kind: "tool_batch", Calls: []agentcontract.ToolCall{
				{CallRef: "read-a", Capability: "records.get", Arguments: agentcontract.JSON{"id": "A"}, Reason: "Read independent record A"},
				{CallRef: "read-b", Capability: "records.get", Arguments: agentcontract.JSON{"id": "B"}, Reason: "Read independent record B"},
			}}}}
			r := &AgentRuntime{Provider: p, Model: model, Grants: map[string]bool{"records.get": true}, MaxConcurrentTools: 2}
			result, err := r.Run(ctx, "Read both independent records")
			wantFacts := 1
			if sibling == "succeeded" {
				wantFacts = 2
			}
			if !errors.Is(err, context.Canceled) || result.Status == "completed" || result.AnswerMarkdown != "" || p.calls != 2 || model.calls != 1 || len(result.Facts) != wantFacts || len(result.Observations) != 2 {
				t.Fatalf("cancellation discarded returned evidence or continued execution: status=%s facts=%d observations=%d provider=%d model=%d error=%v", result.Status, len(result.Facts), len(result.Observations), p.calls, model.calls, err)
			}
			if result.Observations[0].Status != "succeeded" || result.Observations[0].FactID == nil || result.Facts[0].Value["data"].(agentcontract.JSON)["id"] != "A" {
				t.Fatal("first successful result was not retained in call order")
			}
			last := result.Observations[1]
			if sibling == "unknown" && (result.Status != "needs_reconciliation" || last.ErrorCode == nil || *last.ErrorCode != "provider_outcome_unknown") {
				t.Fatal("interrupted sibling became a definite business failure")
			}
			if sibling == "failed" && (last.Status != "failed" || last.ErrorCode == nil || *last.ErrorCode != "business_failed") {
				t.Fatal("explicit sibling failure was replaced by cancellation")
			}
			if sibling == "succeeded" && last.Status != "succeeded" {
				t.Fatal("second known success was replaced by cancellation")
			}
		})
	}
}

func TestTransientParallelComputeKeepsUncertainResultSemantics(t *testing.T) {
	for _, tc := range []struct {
		effect string
		code   string
		status string
	}{
		{"compute", "upstream_response_invalid", "needs_reconciliation"},
		{"compute", "upstream_unavailable", "needs_reconciliation"},
		{"compute", "business_failed", "completed"},
		{"read", "upstream_response_invalid", "completed"},
		{"read", "upstream_unavailable", "completed"},
	} {
		t.Run(tc.effect+"/"+tc.code, func(t *testing.T) {
			cap := agentcontract.CapabilityDescription{Name: "records.get", Effect: tc.effect, Replay: "idempotent", InputSchema: agentcontract.JSON{"type": "object"},
				OutputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"count": agentcontract.JSON{"type": "integer"}}, "required": []string{"count"}}}
			p := &parallelTestProvider{&hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}, hook: func(_ context.Context, _ string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
				if args["id"] == "A" {
					if tc.code == "upstream_response_invalid" {
						return agentcontract.CapabilityResult{Data: agentcontract.JSON{"count": "1"}}, nil
					}
					return agentcontract.CapabilityResult{ErrorCode: tc.code}, nil
				}
				return agentcontract.CapabilityResult{Data: agentcontract.JSON{"count": 2}}, nil
			}}}
			for _, limit := range []int{1, 2} {
				p.calls = 0
				model := &hostModel{decisions: []agentcontract.Decision{{Kind: "tool_batch", Calls: []agentcontract.ToolCall{
					{CallRef: "read-a", Capability: cap.Name, Arguments: agentcontract.JSON{"id": "A"}, Reason: "Independent operation A"},
					{CallRef: "read-b", Capability: cap.Name, Arguments: agentcontract.JSON{"id": "B"}, Reason: "Independent operation B"},
				}}}}
				r := &AgentRuntime{Provider: p, Model: model, Grants: map[string]bool{cap.Name: true}, MaxConcurrentTools: limit}
				result, err := r.Run(t.Context(), "Execute both independent operations")
				wantModelCalls := 2
				if tc.status == "needs_reconciliation" {
					wantModelCalls = 1
				}
				if err != nil || result.Status != tc.status || p.calls != 2 || model.calls != wantModelCalls || len(result.Facts) != 1 || len(result.Observations) != 2 {
					t.Fatalf("concurrency changed uncertain result semantics: limit=%d status=%s provider=%d model=%d facts=%d observations=%d error=%v", limit, result.Status, p.calls, model.calls, len(result.Facts), len(result.Observations), err)
				}
				if result.Observations[0].ErrorCode == nil || *result.Observations[0].ErrorCode != tc.code || result.Observations[1].Status != "succeeded" {
					t.Fatal("partial known and uncertain results were replaced")
				}
			}
		})
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
