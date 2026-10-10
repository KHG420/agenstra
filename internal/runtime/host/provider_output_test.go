package host

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestHostInvalidCustomProviderWriteOutputRequiresReconciliation(t *testing.T) {
	store := testStore(t)
	cap := agentcontract.CapabilityDescription{Name: "records.get", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"}, OutputSchema: agentcontract.JSON{
		"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []string{"id"},
	}}
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}, hook: func(context.Context, string, map[string]any, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		// The external write may have committed; its malformed response cannot
		// establish success or a definite failure that permits another write.
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": 7}}, nil
	}}
	m := &hostModel{decisions: []agentcontract.Decision{callDecision(cap.Name)}}
	h := testHost(t, store, p, m)
	run := createTestHostRun(t, h)
	for range 2 {
		var err error
		run, err = h.Drive(t.Context(), run.RunID, "alice")
		if err != nil || run.Status != "needs_reconciliation" || p.calls != 1 || m.calls != 1 {
			t.Fatalf("invalid write output settled or replayed: status=%s calls=%d model=%d error=%v", run.Status, p.calls, m.calls, err)
		}
		state, err := h.Restore(run)
		if err != nil || len(state.Facts) != 0 || len(state.Pending) != 1 || state.Pending[0].Status != "unknown" || state.Pending[0].ErrorCode == nil || *state.Pending[0].ErrorCode != "upstream_response_invalid" {
			t.Fatal("invalid output became persisted success evidence", state, err)
		}
	}
}
