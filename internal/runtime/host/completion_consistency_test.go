package host

import (
	"context"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestHostCompletionReviewAfterApprovalDoesNotReplayWrite(t *testing.T) {
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {Name: "records.get", Effect: "write", Replay: "never", ApprovalRequired: true, InputSchema: agentcontract.JSON{"type": "object"}}}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision("records.get")}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_approval" || p.calls != 0 {
		t.Fatal("write ran before approval", err, run.Status)
	}
	s, callErr7 := h.Restore(run)
	if callErr7 != nil {
		t.Error(callErr7)
	}
	item := s.Pending[0]
	run, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	h.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		answer := "Could not create"
		if packet.CompletionReview != nil {
			answer = "Created R-1"
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: []string{packet.Facts[0].FactID}}, nil
	})
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	s, restoreErr := h.Restore(run)
	if err != nil || restoreErr != nil || run.Status != "completed" || s.AnswerMarkdown != "Created R-1" || p.calls != 1 || len(s.InvocationReceipts) != 1 || s.InvocationReceipts[0].Status != "succeeded" {
		t.Fatal("approval/review did not preserve exactly one write", err, restoreErr, run.Status, s, p.calls)
	}
	telemetry, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
	if err != nil || telemetry.Budget.UsageByPurpose["completion_review"].Requests != 1 {
		t.Fatal("review missing from persisted telemetry", err, telemetry.Budget)
	}
	if got := (&reactcore.AgentRuntime{Provider: p, Grants: map[string]bool{"records.get": true}}).Context(s).ActionOutcomes; len(got) != 1 || !got[0].ApprovalRequired {
		t.Fatal("completed approved action lost its enforced approval requirement", got)
	}
}

func TestAsyncCompletionReviewUsesTerminalPollReceipt(t *testing.T) {
	h, p, now := asyncReceiptHost(t, func(agentcontract.JSON) agentcontract.JSON {
		return agentcontract.JSON{"id": "private-job-token", "status": "succeeded"}
	}, &agentcontract.ModelOutput{Paths: [][]string{{"status"}}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" {
		t.Fatal(run.Status, err)
	}
	initial, callErr9 := h.Restore(run)
	if callErr9 != nil {
		t.Error(callErr9)
	}
	initialFact := initial.Facts[0].FactID
	*now += 2
	h.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		outcomes := packet.ActionOutcomes
		if len(outcomes) != 1 || outcomes[0].Status != "succeeded" || outcomes[0].FactID == initialFact || outcomes[0].FactID != packet.Facts[0].FactID {
			t.Fatal("review used submission receipt instead of final poll", outcomes, packet.Facts)
		}
		raw, callErr10 := agentcontract.CanonicalJSON(packet)
		if callErr10 != nil {
			t.Error(callErr10)
		}
		if strings.Contains(string(raw), "private-job-token") {
			t.Fatal("poll binding leaked through completion review")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Operation completed", FactIDs: []string{outcomes[0].FactID}}, nil
	})
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 2 {
		t.Fatal("async review replayed an action or lost completion", err, run.Status, p.calls)
	}
}
