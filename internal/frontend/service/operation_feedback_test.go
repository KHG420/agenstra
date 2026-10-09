package service

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestBrowserContextFailurePointsToReceiptWithRefreshCause(t *testing.T) {
	var afterFailure agentcontract.ContextPacket
	model := &hostModel{decisions: browserDecisions()}
	model.hook = func(packet agentcontract.ContextPacket) {
		for _, observation := range packet.Observations {
			if observation.ErrorCode != nil && *observation.ErrorCode == "operation_failed" {
				afterFailure = packet
			}
		}
	}
	f := newWebFixture(t, model, false)
	run := f.run(t)
	command := f.dispatch(t)
	if _, err := f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, agentcontract.JSON{"page": "changed"}); err != nil {
		t.Fatal(err)
	}
	accepted, failed, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1)
	if err != nil || accepted || failed.ErrorCode != "browser_context_changed" {
		t.Fatal(accepted, failed, err)
	}
	f.now += 2
	if _, err = f.h.Drive(t.Context(), run.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	if afterFailure.Progress == nil || len(afterFailure.Progress.Blocked) != 1 {
		t.Fatal("missing failed action", afterFailure.Progress)
	}
	blocked := afterFailure.Progress.Blocked[0]
	if blocked.FactID == nil || *blocked.FactID == "" {
		t.Fatal("failure has no reference to its actual operation receipt", blocked)
	}
	found := false
	for _, fact := range afterFailure.Facts {
		if fact.FactID == *blocked.FactID {
			data := fact.Value["data"].(map[string]any)
			found = fact.SourceCapability == "ui.command_status" && data["status"] == "failed" && data["error_code"] == "browser_context_changed"
		}
	}
	if !found {
		t.Fatal("failed action cites unavailable or unrelated evidence", blocked)
	}
}
