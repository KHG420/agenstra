package engine

import "testing"

func TestBrowserContextFailurePointsToReceiptWithRefreshCause(t *testing.T) {
	var afterFailure ContextPacket
	model := &hostModel{decisions: browserDecisions()}
	model.hook = func(packet ContextPacket) {
		for _, observation := range packet.Observations {
			if observation.ErrorCode != nil && *observation.ErrorCode == "operation_failed" {
				afterFailure = packet
			}
		}
	}
	f := newWebFixture(t, model, false)
	run := f.run(t)
	command := f.dispatch(t)
	if _, err := f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, JSON{"page": "changed"}); err != nil {
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

func TestOperationFailureFeedbackKeepsBusinessAndUnknownOutcomesDistinct(t *testing.T) {
	for _, tc := range []struct{ capability, poll, status, code string }{
		{"job.start", "job.status", "failed", "browser_context_changed"},
		{"ui.write", "ui.command_status", "failed", "business_revision_conflict"},
		{"ui.write", "ui.command_status", "unknown", "browser_context_changed"},
	} {
		t.Run(tc.capability+"/"+tc.status, func(t *testing.T) {
			state := &RuntimeState{}
			item := &Invocation{Call: ToolCall{CallRef: "call-1", Capability: tc.capability}}
			fact := Fact{FactID: NewID(), Value: JSON{"data": JSON{"command_id": "command-1", "status": tc.status, "error_code": tc.code}}}
			binding := OperationBinding{IDPath: []any{"command_id"}, StatusPath: []any{"status"}, PollCapability: tc.poll, PollArgument: []string{"command_id"}, FailureStates: []string{"failed"}, ReconciliationStates: []string{"unknown"}}
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
			if item.Status != "failed" || *observation.ErrorCode != "operation_failed" || *observation.FactID != fact.FactID || len(observation.Arguments) != 0 {
				t.Fatal("business failure lost its evidence or received browser retry advice", observation)
			}
		})
	}
}
