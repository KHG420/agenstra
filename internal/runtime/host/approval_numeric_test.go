package host

import (
	"context"
	"encoding/json"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestHostApprovalPreservesExactNumericArguments(t *testing.T) {
	for _, tc := range []struct{ original, other string }{
		{"9007199254740993.0", "9007199254740992.0"},
		{"0.100000000000000000001", "0.1"},
		{"1e-400", "0.0"},
	} {
		t.Run(tc.original, func(t *testing.T) {
			call := callDecision("records.get")
			call.Calls[0].Arguments = agentcontract.JSON{"value": json.Number(tc.original)}
			provider := &hostProvider{
				caps: map[string]agentcontract.CapabilityDescription{"records.get": {
					Name: "records.get", Version: "1", Description: "Compute",
					InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"value": agentcontract.JSON{"type": "number"}}, "required": []any{"value"}},
					Effect:      "compute", Replay: "never", ReferenceScope: "durable", ApprovalRequired: true,
				}},
				hook: func(_ context.Context, _ string, args map[string]any, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
					if value, ok := args["value"].(json.Number); !ok || string(value) != tc.original {
						t.Fatalf("approved numerical value changed before execution: %#v", args["value"])
					}
					return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "R-1"}, ReferenceScope: "durable"}, nil
				},
			}
			h := testHost(t, testStore(t), provider, &hostModel{decisions: []agentcontract.Decision{call}})
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "needs_approval" || provider.calls != 0 {
				t.Fatalf("approval not required before execution: %s, %v, calls=%d", run.Status, err, provider.calls)
			}
			state, err := h.Restore(run)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Pending) != 1 {
				t.Fatalf("missing persisted invocation: %+v", state.Pending)
			}
			item := state.Pending[0]
			if value, ok := item.Call.Arguments["value"].(json.Number); !ok || string(value) != tc.original {
				t.Fatalf("pending numerical value changed during persistence: %#v", item.Call.Arguments["value"])
			}
			other := item.Call
			other.Arguments = agentcontract.JSON{"value": json.Number(tc.other)}
			_, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, reactcore.ArgumentsDigest(other), run.Revision, true)
			if agentcontract.ErrorCode(err) != "approval_arguments_changed" || provider.calls != 0 {
				t.Fatalf("approval for a different exact value accepted: %v, calls=%d", err, provider.calls)
			}
			run, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true)
			if err != nil {
				t.Fatal(err)
			}
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "completed" || provider.calls != 1 {
				t.Fatalf("approved call did not execute once: %s, %v, calls=%d", run.Status, err, provider.calls)
			}
		})
	}
}
