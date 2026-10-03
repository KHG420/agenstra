package agenstra

import (
	"crypto/sha256"
	"encoding/hex"
)

func beginInvocationReceipt(item *Invocation, cap CapabilityDescription) {
	if cap.Effect == "read" {
		return
	}
	item.Receipt = &InvocationReceipt{
		InvocationID: item.InvocationID, Capability: item.Call.Capability,
		Effect: cap.Effect, ArgumentsSHA256: item.ArgumentsSHA256, Status: "unknown",
	}
}

func captureInvocationReceipt(item *Invocation, cap CapabilityDescription, outcome CallOutcome, result []byte) {
	if cap.Effect == "read" {
		return
	}
	beginInvocationReceipt(item, cap)
	r := item.Receipt
	r.Reconciled = item.Reconciled
	if outcome.Fact != nil {
		r.Status = "succeeded"
		r.FactID = outcome.Fact.FactID
		sum := sha256.Sum256(result)
		r.ResultSHA256, r.ResultBytes = hex.EncodeToString(sum[:]), len(result)
		if cap.Operation != nil {
			id, valueErr := operationValue(outcome.Fact.Value["data"], cap.Operation.IDPath)
			status, statusErr := operationValue(outcome.Fact.Value["data"], cap.Operation.StatusPath)
			var idErr error
			r.OperationID, idErr = scalarValue(id)
			r.OperationStatus, _ = status.(string)
			switch {
			case valueErr != nil || idErr != nil || statusErr != nil || r.OperationStatus == "" || len(r.OperationID) > 256 || len(r.OperationStatus) > 128:
				r.OperationID, r.OperationStatus = "", ""
				r.Status, r.ErrorCode = "unknown", "operation_contract_invalid"
			case containsString(cap.Operation.PendingStates, r.OperationStatus):
				r.Status = "accepted"
			case containsString(cap.Operation.FailureStates, r.OperationStatus):
				r.Status, r.ErrorCode = "failed", "operation_failed"
			case !containsString(cap.Operation.SuccessStates, r.OperationStatus):
				r.Status, r.ErrorCode = "unknown", "operation_outcome_unknown"
			}
		}
	} else {
		r.ErrorCode = outcome.ErrorCode
		if !unknownOutcome(outcome.ErrorCode) {
			r.Status = "failed"
		}
	}
}

func (h *AgentHost) failOperationResultStorage(run StoredRun, state *RuntimeState, item *Invocation, code string) (StoredRun, error) {
	if item.Receipt != nil {
		item.Receipt.FactID, item.Receipt.ResultErrorCode = "", code
		switch item.Receipt.Status {
		case "succeeded", "failed":
			item.Status = item.Receipt.Status
		default:
			item.Status = "unknown"
		}
	}
	state.Status, state.ErrorCode = "failed", strptr(code)
	return h.save(run, state, "", nil, JSON{"kind": "operation_result_storage_failed", "invocation_id": item.InvocationID, "error_code": code})
}

func unsettledInvocation(item Invocation) bool {
	return item.Status == "unknown" || item.Status == "in_flight" || item.PollInFlight || (item.Status == "waiting" && item.Operation != nil)
}

func runHasInvocationEvidence(run StoredRun) bool {
	runtime, _ := run.State["runtime"].(map[string]any)
	if receipts, _ := runtime["invocation_receipts"].([]any); len(receipts) > 0 {
		return true
	}
	items, _ := runtime["pending"].([]any)
	for _, value := range items {
		item, _ := value.(map[string]any)
		if item["status"] == "unknown" || item["status"] == "in_flight" || item["poll_in_flight"] == true || (item["status"] == "waiting" && item["operation"] != nil) {
			return true
		}
	}
	return false
}

func checkpointInvocationReceipts(state *RuntimeState) {
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
