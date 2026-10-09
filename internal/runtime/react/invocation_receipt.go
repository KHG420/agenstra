package react

import (
	"crypto/sha256"
	"encoding/hex"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// BeginInvocationReceipt records the attempted call identity before external IO.
func BeginInvocationReceipt(item *agentcontract.Invocation, cap agentcontract.CapabilityDescription) {
	if cap.Effect == "read" {
		return
	}
	item.Receipt = &agentcontract.InvocationReceipt{
		InvocationID: item.InvocationID, Capability: item.Call.Capability,
		Effect: cap.Effect, ArgumentsSHA256: item.ArgumentsSHA256, Status: "unknown",
	}
}

// CaptureInvocationReceipt retains the provider result separately from later orchestration or storage failure.
func CaptureInvocationReceipt(item *agentcontract.Invocation, cap agentcontract.CapabilityDescription, outcome agentcontract.CallOutcome, result []byte) {
	if cap.Effect == "read" {
		return
	}
	BeginInvocationReceipt(item, cap)
	r := item.Receipt
	r.Reconciled = item.Reconciled
	if outcome.Fact != nil {
		r.Status = "succeeded"
		r.ErrorCode = ""
		r.FactID = outcome.Fact.FactID
		sum := sha256.Sum256(result)
		r.ResultSHA256, r.ResultBytes = hex.EncodeToString(sum[:]), len(result)
		if cap.Operation != nil {
			id, valueErr := OperationValue(outcome.Fact.Value["data"], cap.Operation.IDPath)
			status, statusErr := OperationValue(outcome.Fact.Value["data"], cap.Operation.StatusPath)
			var idErr error
			r.OperationID, idErr = agentcontract.ScalarValue(id)
			r.OperationStatus, _ = status.(string)
			switch {
			case valueErr != nil || idErr != nil || statusErr != nil || r.OperationStatus == "" || len(r.OperationID) > 256 || len(r.OperationStatus) > 128:
				r.OperationID, r.OperationStatus = "", ""
				r.Status, r.ErrorCode = "unknown", "operation_contract_invalid"
			case agentcontract.ContainsString(cap.Operation.PendingStates, r.OperationStatus):
				r.Status = "accepted"
			case agentcontract.ContainsString(cap.Operation.FailureStates, r.OperationStatus):
				r.Status, r.ErrorCode = "failed", "operation_failed"
			case !agentcontract.ContainsString(cap.Operation.SuccessStates, r.OperationStatus):
				r.Status, r.ErrorCode = "unknown", "operation_outcome_unknown"
			}
		}
	} else {
		r.ErrorCode = outcome.ErrorCode
		if !UnknownOutcome(outcome.ErrorCode) {
			r.Status = "failed"
		}
	}
}
