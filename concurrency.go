package agenstra

import (
	"context"
	"sync"
	"time"
)

type preparedInvocation struct {
	cap    CapabilityDescription
	inv    InvocationContext
	grants map[string]bool
}

type parallelInvocation struct {
	call   ToolCall
	inv    InvocationContext
	grants map[string]bool
}

func concurrentToolLimit(limit int) int {
	if limit == 0 {
		return 4
	}
	return max(1, min(4, limit))
}

func independentBatch(provider CapabilityProvider, pending []Invocation, policy ExecutionPolicy, limit int) bool {
	concurrent, ok := provider.(ConcurrentCapabilityProvider)
	if !ok || concurrentToolLimit(limit) < 2 {
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

// Workers only perform IO into isolated result slots. State and checkpoints are
// applied serially in the original call order after every worker has exited.
func invokeParallel(ctx context.Context, provider CapabilityProvider, tasks []parallelInvocation, limit int, timeout time.Duration) []CallOutcome {
	outcomes := make([]CallOutcome, len(tasks))
	semaphore := make(chan struct{}, concurrentToolLimit(limit))
	var group sync.WaitGroup
	for i, task := range tasks {
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				outcomes[i] = CallOutcome{ErrorCode: "provider_outcome_unknown"}
				return
			}
			if ctx.Err() != nil {
				outcomes[i] = CallOutcome{ErrorCode: "provider_outcome_unknown"}
				return
			}
			callCtx := ctx
			cancel := func() {}
			if timeout > 0 {
				callCtx, cancel = context.WithTimeout(ctx, timeout)
			}
			defer cancel()
			outcome, err := ExecuteCall(callCtx, provider, task.grants, task.call, &task.inv)
			if err != nil {
				outcome = CallOutcome{ErrorCode: "provider_outcome_unknown"}
			}
			outcomes[i] = outcome
		}()
	}
	group.Wait()
	return outcomes
}

func (h *AgentHost) parallelBatch(ctx context.Context, run StoredRun, state *RuntimeState, provider CapabilityProvider) (bool, error) {
	if _, ok := provider.(ConcurrentCapabilityProvider); !ok || concurrentToolLimit(h.Settings.MaxConcurrentTools) < 2 || len(state.Pending) < 2 {
		return false, nil
	}
	policy, err := h.projectPolicy(ctx, run)
	if err != nil {
		return false, err
	}
	return independentBatch(provider, state.Pending, policy, h.Settings.MaxConcurrentTools), nil
}

func (h *AgentHost) executeParallel(ctx context.Context, run StoredRun, state *RuntimeState, provider CapabilityProvider, runtime *AgentRuntime) (StoredRun, error) {
	var tasks []parallelInvocation
	var prepared []preparedInvocation
	var indices []int
	for i := range state.Pending {
		item := &state.Pending[i]
		if item.Status == "succeeded" || item.Status == "failed" {
			continue
		}
		var call *preparedInvocation
		var err error
		run, call, err = h.prepareInvocation(ctx, run, state, item, provider, runtime)
		if err != nil {
			return run, err
		}
		if call == nil {
			// Nothing was dispatched. Undo reservations if fresh authorization or
			// reference checks stop preparation of a later call.
			for _, index := range indices {
				state.Pending[index].Status = "prepared"
				state.Pending[index].Attempts--
			}
			return h.save(run, state, "", nil, JSON{"kind": "parallel_preparation_stopped"})
		}
		indices = append(indices, i)
		prepared = append(prepared, *call)
		tasks = append(tasks, parallelInvocation{call: item.Call, inv: call.inv, grants: call.grants})
	}
	outcomes := invokeParallel(ctx, provider, tasks, h.Settings.MaxConcurrentTools, time.Duration(h.Settings.InvocationTimeoutSeconds*1e9))
	if ctx.Err() != nil {
		return run, ctx.Err()
	}
	stopStatus := "running"
	var stopCode *string
	var wake *float64
	priority := map[string]int{"running": 0, "waiting": 1, "failed": 2, "needs_authorization": 3, "needs_reconciliation": 4}
	for i, index := range indices {
		var err error
		run, err = h.settleInvocation(run, state, &state.Pending[index], prepared[i], outcomes[i])
		if err != nil {
			return run, err
		}
		if priority[state.Status] > priority[stopStatus] {
			stopStatus, stopCode = state.Status, state.ErrorCode
		}
		if run.NextWakeAt != nil && (wake == nil || *run.NextWakeAt < *wake) {
			v := *run.NextWakeAt
			wake = &v
		}
	}
	state.Status, state.ErrorCode = stopStatus, stopCode
	if stopStatus != "waiting" {
		wake = nil
	}
	return h.save(run, state, "", wake, JSON{"kind": "parallel_finished", "count": len(tasks)})
}
