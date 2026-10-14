package host

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompactCompletionApprovalPersistsAfterAuthorizedWrite(t *testing.T) {
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {Name: "records.get", Effect: "write", Replay: "never", ApprovalRequired: true, InputSchema: agentcontract.JSON{"type": "object"}}}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision("records.get")}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_approval" || p.calls != 0 {
		t.Fatal("write ran before approval", err, run.Status)
	}
	s, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	item := s.Pending[0]
	run, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "Verified record creation"
	h.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if len(packet.Facts) != 1 {
			t.Fatal("authorized write lost evidence")
		}
		if packet.CompletionReview != nil {
			return agentcontract.Decision{Kind: "approve_completion"}, nil
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: want, FactIDs: []string{packet.Facts[0].FactID}}, nil
	})
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	s, err = h.Restore(run)
	if err != nil || run.Status != "completed" || s.AnswerMarkdown != want || p.calls != 1 || len(s.InvocationReceipts) != 1 || s.InvocationReceipts[0].Status != "succeeded" {
		t.Fatal("compact approval lost persisted answer or replayed write", err, run.Status, p.calls)
	}
	if s.Decisions[len(s.Decisions)-1]["kind"] != "approve_completion" {
		t.Fatal("compact verdict was not checkpointed")
	}
	telemetry, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
	if err != nil || telemetry.Budget.UsageByPurpose["completion_review"].Requests != 1 {
		t.Fatal("compact verdict lost persisted model accounting", err)
	}
}
