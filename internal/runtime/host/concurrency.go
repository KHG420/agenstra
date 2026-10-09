package host

import (
	"context"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

type preparedInvocation struct {
	cap    agentcontract.CapabilityDescription
	inv    agentcontract.InvocationContext
	grants map[string]bool
}

func (h *AgentHost) parallelBatch(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, provider agentcontract.CapabilityProvider) (bool, error) {
	if _, ok := provider.(agentcontract.ConcurrentCapabilityProvider); !ok || reactcore.ConcurrentToolLimit(h.runSettings(run).MaxConcurrentTools) < 2 || len(state.Pending) < 2 {
		return false, nil
	}
	policy, err := h.ProjectPolicy(ctx, run)
	if err != nil {
		return false, err
	}
	return reactcore.IndependentBatch(provider, state.Pending, policy, h.runSettings(run).MaxConcurrentTools), nil
}

func (h *AgentHost) executeParallel(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, provider agentcontract.CapabilityProvider, runtime *reactcore.AgentRuntime) (agentcontract.StoredRun, error) {
	var tasks []reactcore.ParallelInvocation
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
			return h.save(run, state, "", nil, agentcontract.JSON{"kind": "parallel_preparation_stopped"})
		}
		indices = append(indices, i)
		prepared = append(prepared, *call)
		tasks = append(tasks, reactcore.ParallelInvocation{Call: item.Call, Inv: call.inv, Grants: call.grants})
	}
	outcomes := reactcore.InvokeParallel(ctx, provider, tasks, h.runSettings(run).MaxConcurrentTools, time.Duration(h.runSettings(run).InvocationTimeoutSeconds*1e9))
	stopStatus := "running"
	var stopCode *string
	var wake *float64
	priority := map[string]int{"running": 0, "waiting": 1, "failed": 2, "needs_authorization": 3, "needs_reconciliation": 4}
	for i, index := range indices {
		if ctx.Err() != nil && outcomes[i].Fact == nil && reactcore.UnknownOutcome(outcomes[i].ErrorCode) {

			// Preserve interrupted reservations for the normal durable recovery
			// path, while still recording responses that have a known outcome.
			continue
		}
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
	if ctx.Err() != nil {
		return run, ctx.Err()
	}
	return h.save(run, state, "", wake, agentcontract.JSON{"kind": "parallel_finished", "count": len(tasks)})
}
