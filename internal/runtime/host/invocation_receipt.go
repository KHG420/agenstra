package host

import (
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func (h *AgentHost) failOperationResultStorage(run agentcontract.StoredRun, state *agentcontract.RuntimeState, item *agentcontract.Invocation, code string) (agentcontract.StoredRun, error) {
	if item.Receipt != nil {
		item.Receipt.FactID, item.Receipt.ResultErrorCode = "", code
		switch item.Receipt.Status {
		case "succeeded", "failed":
			item.Status = item.Receipt.Status
		default:
			item.Status = "unknown"
		}
	}
	state.Status, state.ErrorCode = "failed", agentcontract.Strptr(code)
	return h.save(run, state, "", nil, agentcontract.JSON{"kind": "operation_result_storage_failed", "invocation_id": item.InvocationID, "error_code": code})
}

func unsettledInvocation(item agentcontract.Invocation) bool {
	return item.Status == "unknown" || item.Status == "in_flight" || item.PollInFlight || (item.Status == "waiting" && item.Operation != nil)
}

func checkpointInvocationReceipts(state *agentcontract.RuntimeState) {
	for _, item := range state.Pending {
		if item.Receipt == nil {
			continue
		}
		replaced := false
		for i := range state.InvocationReceipts {
			if state.InvocationReceipts[i].InvocationID == item.InvocationID {
				state.InvocationReceipts[i] = *item.Receipt
				replaced = true
				break
			}
		}
		if !replaced {
			state.InvocationReceipts = append(state.InvocationReceipts, *item.Receipt)
		}
	}
}
