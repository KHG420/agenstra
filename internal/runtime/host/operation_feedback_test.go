package host

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestOperationFailureFeedbackKeepsBusinessAndUnknownOutcomesDistinct(t *testing.T) {
	for _, tc := range []struct{ capability, poll, status, code string }{
		{"job.start", "job.status", "failed", "browser_context_changed"},
		{"ui.write", "ui.command_status", "failed", "business_revision_conflict"},
		{"ui.write", "ui.command_status", "unknown", "browser_context_changed"},
	} {
		t.Run(tc.capability+"/"+tc.status, func(t *testing.T) {
			state := &agentcontract.RuntimeState{}
			item := &agentcontract.Invocation{Call: agentcontract.ToolCall{CallRef: "call-1", Capability: tc.capability}}
			fact := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"command_id": "command-1", "status": tc.status, "error_code": tc.code}}}
			binding := agentcontract.OperationBinding{IDPath: []any{"command_id"}, StatusPath: []any{"status"}, PollCapability: tc.poll, PollArgument: []string{"command_id"}, FailureStates: []string{"failed"}, ReconciliationStates: []string{"unknown"}}
			if err := (&AgentHost{}).operation(state, item, fact, binding); err != nil {
				t.Fatal(err)
			}
			if tc.status == "unknown" {
				if state.Status != "needs_reconciliation" || len(state.Observations) != 0 {
					t.Fatal("uncertain result was presented as a retryable failure", state)
				}
				return
			}
			observation := state.ModelObservations[0]
			if item.Status != "failed" || *observation.ErrorCode != "operation_failed" || *observation.FactID != fact.FactID || len(observation.Arguments) != 0 || !observation.ArgumentsOmitted {
				t.Fatal("business failure lost its evidence or received browser retry advice", observation)
			}
		})
	}
}
