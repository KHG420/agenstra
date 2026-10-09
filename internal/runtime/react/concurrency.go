package react

import (
	"context"
	"sync"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// ParallelInvocation contains one fully prepared call and its trusted invocation identity and grants. Workers only write isolated result slots.
type ParallelInvocation struct {
	Call   agentcontract.ToolCall
	Inv    agentcontract.InvocationContext
	Grants map[string]bool
}

// ConcurrentToolLimit normalizes the existing one-to-four per-run tool limit.
func ConcurrentToolLimit(limit int) int {
	if limit == 0 {
		return 4
	}
	return max(1, min(4, limit))
}

// IndependentBatch checks that every pending call can execute concurrently under its effect, replay, approval and live-grant contracts.
func IndependentBatch(provider agentcontract.CapabilityProvider, pending []agentcontract.Invocation, policy agentcontract.ExecutionPolicy, limit int) bool {
	concurrent, ok := provider.(agentcontract.ConcurrentCapabilityProvider)
	if !ok || ConcurrentToolLimit(limit) < 2 {
		return false
	}
	count := 0
	for _, item := range pending {
		if item.Status == "succeeded" || item.Status == "failed" {
			continue
		}
		cap, ok := provider.Capabilities()[item.Call.Capability]
		if !ok || item.Status != "prepared" || item.Attempts != 0 || cap.Operation != nil || (cap.Effect != "read" && cap.Effect != "compute") || (cap.Replay != "safe" && cap.Replay != "idempotent") || cap.ApprovalRequired || policy.ApprovalCapabilities[cap.Name] || !policy.GrantedCapabilities[cap.Name] || !concurrent.ConcurrentInvocation(cap.Name) {
			return false
		}
		count++
	}
	return count > 1
}

// InvokeParallel joins bounded IO workers and returns results in call order. Drivers retain exclusive ownership of checkpoints.
// Workers only perform IO into isolated result slots. State and checkpoints are
// applied serially in the original call order after every worker has exited.
func InvokeParallel(ctx context.Context, provider agentcontract.CapabilityProvider, tasks []ParallelInvocation, limit int, timeout time.Duration) []agentcontract.CallOutcome {
	outcomes := make([]agentcontract.CallOutcome, len(tasks))
	semaphore := make(chan struct{}, ConcurrentToolLimit(limit))
	var group sync.WaitGroup
	for i, task := range tasks {
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				outcomes[i] = agentcontract.CallOutcome{ErrorCode: "provider_outcome_unknown"}
				return
			}
			if ctx.Err() != nil {
				outcomes[i] = agentcontract.CallOutcome{ErrorCode: "provider_outcome_unknown"}
				return
			}
			callCtx := ctx
			cancel := func() {}
			if timeout > 0 {
				callCtx, cancel = context.WithTimeout(ctx, timeout)
			}
			defer cancel()
			outcome, err := ExecuteCall(callCtx, provider, task.Grants, task.Call, &task.Inv)
			if err != nil {
				outcome = agentcontract.CallOutcome{ErrorCode: "provider_outcome_unknown"}
			}
			outcomes[i] = outcome
		}()
	}
	group.Wait()
	return outcomes
}
