package service

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestBrowserContextReferenceSurvivesAnUnchangedLivePage(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)

	// Capture 45 seconds before the simulated approval, while the page is online.
	f.now -= 45
	var err error
	f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, agentcontract.JSON{"page": "home"})
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
	result, err := provider.Invoke(t.Context(), "ui.get_context", agentcontract.JSON{}, &agentcontract.InvocationContext{OwnerID: "alice", RunID: run.RunID, InvocationID: agentcontract.NewID()})
	if err != nil || result.ErrorCode != "" {
		t.Fatal(result, err)
	}
	f.now += 45
	if _, _, err = f.w.PollBrowser("alice", f.session.ID, f.key, 1); err != nil {
		t.Fatal(err)
	}
	id := agentcontract.NewID()
	fact := agentcontract.Fact{FactID: id, Value: agentcontract.JSON{"data": result.Data}, ReferenceScope: result.ReferenceScope, ExpiresAt: result.ExpiresAt}
	if !reactcore.ReferenceAvailable(fact, "") {
		t.Fatal("unchanged live page reference expired during approval")
	}
	resolved, err := reactcore.ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": id, "path": []any{"data", "context", "page"}}}, map[string]agentcontract.Fact{id: fact}, "", true)
	if err != nil || resolved != "home" {
		t.Fatal("approved argument could not resolve its original page snapshot", resolved, err)
	}
}

func TestBrowserEnqueueRechecksRevisionAfterInvocationValidation(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "new command"
		if cached {
			name = "original receipt"
		}
		t.Run(name, func(t *testing.T) {
			f := newWebFixture(t, &hostModel{}, false)
			run, err := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Open orders", "enqueue-revision")
			if err != nil {
				t.Fatal(err)
			}
			provider, err := f.h.ProviderFactory(t.Context(), "alice", "records-web")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := provider.Close(); err != nil {
					t.Error(err)
				}
			}()
			inv := &agentcontract.InvocationContext{OwnerID: "alice", RunID: run.RunID, InvocationID: agentcontract.NewID()}
			contextResult, err := provider.Invoke(t.Context(), "ui.get_context", agentcontract.JSON{}, inv)
			if err != nil || contextResult.ErrorCode != "" {
				t.Fatal(contextResult, err)
			}
			if err := provider.(agentcontract.InvocationValidator).ValidateInvocation(t.Context(), "ui.navigate", *inv); err != nil {
				t.Fatal(err)
			}
			args := agentcontract.JSON{"page": "orders"}
			var original agentcontract.CapabilityResult
			if cached {
				original, err = provider.Invoke(t.Context(), "ui.navigate", args, inv)
				if err != nil || original.ErrorCode != "" {
					t.Fatal(original, err)
				}
			}
			// The page can change after prepare-time validation but before enqueue.
			f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, agentcontract.JSON{"page": "edited"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := provider.Invoke(t.Context(), "ui.navigate", args, inv)
			if err != nil {
				t.Fatal(err)
			}
			var commands int
			if err := f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_commands").Scan(&commands); err != nil {
				t.Fatal(err)
			}
			if cached {
				if result.ErrorCode != "" || agentcontract.WebHash(result.Data) != agentcontract.WebHash(original.Data) || commands != 1 {
					t.Fatal("page change replaced the original invocation receipt", result, commands)
				}
				return
			}
			if result.ErrorCode != "browser_context_changed" || commands != 0 {
				t.Fatal("stale observation queued a new command", result, commands)
			}
			contextResult, err = provider.Invoke(t.Context(), "ui.get_context", agentcontract.JSON{}, inv)
			if err != nil || contextResult.ErrorCode != "" {
				t.Fatal(contextResult, err)
			}
			result, err = provider.Invoke(t.Context(), "ui.navigate", args, inv)
			if err != nil || result.ErrorCode != "" {
				t.Fatal("fresh observation could not enqueue the original identity", result, err)
			}
			command, err := f.w.command("alice", inv.InvocationID)
			if err != nil || command.ContextRevision != f.session.ContextRevision || command.Status != "queued" {
				t.Fatal(command, err)
			}
		})
	}
}

func TestBrowserApprovalDelayStillChecksPageRevisionAndLiveness(t *testing.T) {
	for _, change := range []string{"unchanged", "edited", "edited_after_enqueue", "offline"} {
		t.Run(change, func(t *testing.T) {
			f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, true)
			run := f.run(t)
			if run.Status != "needs_approval" {
				t.Fatal(run.Status)
			}
			state, err := f.h.Restore(run)
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
				f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, agentcontract.JSON{"page": "changed"})
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
			if change == "edited" {
				var commands int
				if err = f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_commands").Scan(&commands); err != nil || commands != 0 {
					t.Fatal("changed page was queued after approval", commands, err)
				}
				state, err := f.h.Restore(run)
				if err != nil {
					t.Fatal(err)
				}
				rejected := false
				for _, observation := range state.Observations {
					rejected = rejected || (observation.Capability == "ui.navigate" && observation.Status == "failed" && observation.ErrorCode != nil && *observation.ErrorCode == "browser_context_changed")
				}
				if !rejected {
					t.Fatal("approval did not retain the stale-page rejection", state.Observations)
				}
				return
			}
			command := f.dispatch(t)
			if change == "edited_after_enqueue" {
				f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, f.session.ContextRevision, agentcontract.JSON{"page": "changed"})
				if err != nil {
					t.Fatal(err)
				}
			}
			accepted, command, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1)
			if change == "edited_after_enqueue" {
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
	invoke := func(name string, args agentcontract.JSON) agentcontract.CapabilityResult {
		t.Helper()
		result, err := provider.Invoke(t.Context(), name, args, &agentcontract.InvocationContext{OwnerID: "alice", RunID: run.RunID, InvocationID: agentcontract.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := invoke("ui.get_context", agentcontract.JSON{}); result.ErrorCode != "" {
		t.Fatal(result)
	}
	command := f.dispatch(t)
	if ok, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "succeeded", agentcontract.JSON{"page": "orders"}, ""); err != nil {
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
	if _, err := f.w.UpdatePageObservation("alice", resumed.ID, f.key, resumed.Generation, resumed.ContextRevision, agentcontract.JSON{"page": "orders"}); err != nil {
		t.Fatal(err)
	}

	// Resuming a tab may reconnect future actions, but cannot reuse its old page snapshot.
	if result := invoke("ui.navigate", agentcontract.JSON{"page": "details"}); result.ErrorCode != "browser_context_required" {
		t.Fatal(result)
	}
	if result := invoke("ui.get_context", agentcontract.JSON{}); result.ErrorCode != "" {
		t.Fatal(result)
	}
	if result := invoke("ui.navigate", agentcontract.JSON{"page": "details"}); result.ErrorCode != "" {
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
