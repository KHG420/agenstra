package agenstra

import "testing"

func TestBrowserContextReferenceSurvivesAnUnchangedLivePage(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	// Capture 45 seconds before the simulated approval, while the page is online.
	f.now -= 45
	var err error
	f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, JSON{"page": "home"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Open page", "lifetime")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := f.h.ProviderFactory(t.Context(), "alice", "records-web")
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(provider.Close)
	result, err := provider.Invoke(t.Context(), "ui.get_context", JSON{}, &InvocationContext{OwnerID: "alice", RunID: run.RunID, InvocationID: NewID()})
	if err != nil || result.ErrorCode != "" {
		t.Fatal(result, err)
	}
	f.now += 45
	if _, _, err = f.w.PollBrowser("alice", f.session.ID, f.key, 1); err != nil {
		t.Fatal(err)
	}
	id := NewID()
	fact := Fact{FactID: id, Value: JSON{"data": result.Data}, ReferenceScope: result.ReferenceScope, ExpiresAt: result.ExpiresAt}
	if !ReferenceAvailable(fact, "") {
		t.Fatal("unchanged live page reference expired during approval")
	}
	resolved, err := ResolveArgument(JSON{"$fact_value": JSON{"fact_id": id, "path": []any{"data", "context", "page"}}}, map[string]Fact{id: fact}, "", true)
	if err != nil || resolved != "home" {
		t.Fatal("approved argument could not resolve its original page snapshot", resolved, err)
	}
}

func TestBrowserApprovalDelayStillChecksPageRevisionAndLiveness(t *testing.T) {
	for _, change := range []string{"unchanged", "edited", "offline"} {
		t.Run(change, func(t *testing.T) {
			f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, true)
			run := f.run(t)
			if run.Status != "needs_approval" {
				t.Fatal(run.Status)
			}
			state, err := f.h.restore(run)
			if err != nil {
				t.Fatal(err)
			}
			item := state.Pending[0]
			f.now += 45
			if change != "offline" {
				if _, _, err = f.w.PollBrowser("alice", f.session.ID, f.key, 1); err != nil {
					t.Fatal(err)
				}
			}
			if change == "edited" {
				f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, JSON{"page": "changed"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true); err != nil {
				t.Fatal(err)
			}
			run, err = f.h.Drive(t.Context(), run.RunID, "alice")
			if err != nil {
				t.Fatal(err)
			}
			if change == "offline" {
				var commands int
				if err = f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_commands").Scan(&commands); err != nil || commands != 0 {
					t.Fatal("offline approval dispatched", commands, err)
				}
				return
			}
			command := f.dispatch(t)
			accepted, command, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1)
			if change == "edited" {
				if err != nil || accepted || command.ErrorCode != "browser_context_changed" {
					t.Fatal(accepted, command, err)
				}
			} else if err != nil || !accepted {
				t.Fatal(accepted, command, err)
			}
		})
	}
}

func TestBrowserResumeRefreshesRunBindingAndRequiresFreshObservation(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
	run := f.run(t)
	provider, err := f.h.ProviderFactory(t.Context(), "alice", "records-web")
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(provider.Close)
	invoke := func(name string, args JSON) CapabilityResult {
		t.Helper()
		result, err := provider.Invoke(t.Context(), name, args, &InvocationContext{OwnerID: "alice", RunID: run.RunID, InvocationID: NewID()})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := invoke("ui.get_context", JSON{}); result.ErrorCode != "" {
		t.Fatal(result)
	}
	command := f.dispatch(t)
	if ok, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "succeeded", JSON{"page": "orders"}, ""); err != nil {
		t.Fatal(err)
	}

	other, otherKey, err := f.w.CreateBrowserSession(t.Context(), "alice", "records-web", "1", []string{"ui.navigate"})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", other.ID, "Another tab", "other-tab")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := f.w.ResumeBrowserSession("alice", f.session.ID, f.key, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.UpdatePageObservation("alice", resumed.ID, f.key, resumed.Generation, resumed.ContextRevision, JSON{"page": "orders"}); err != nil {
		t.Fatal(err)
	}
	// Resuming a tab may reconnect future actions, but cannot reuse its old page snapshot.
	if result := invoke("ui.navigate", JSON{"page": "details"}); result.ErrorCode != "browser_context_required" {
		t.Fatal(result)
	}
	if result := invoke("ui.get_context", JSON{}); result.ErrorCode != "" {
		t.Fatal(result)
	}
	if result := invoke("ui.navigate", JSON{"page": "details"}); result.ErrorCode != "" {
		t.Fatal(result)
	}
	commands, blocked, err := f.w.PollBrowser("alice", resumed.ID, f.key, resumed.Generation)
	if err != nil || blocked || len(commands) != 1 || commands[0].Generation != resumed.Generation {
		t.Fatal(commands, blocked, err)
	}
	original, err := f.w.command("alice", command.ID)
	if err != nil || original.Generation != 1 || original.Status != "succeeded" {
		t.Fatal(original, err)
	}
	binding, err := f.w.Store.binding("alice", otherRun.RunID)
	if err != nil || binding.Generation != 1 || binding.SessionID != other.ID {
		t.Fatal(binding, err)
	}
	if _, _, err := f.w.PollBrowser("alice", other.ID, otherKey, 1); err != nil {
		t.Fatal(err)
	}
}
