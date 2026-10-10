package service

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestBrowserReceiptRetainsLargeDeclaredResults(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "unknown"} {
		for _, size := range []int{65536, 70 << 10, 1 << 20} {
			t.Run(status+"/"+strconv.Itoa(size), func(t *testing.T) {
				f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
				f.run(t)
				command := f.dispatch(t)
				if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
					t.Fatal(accepted, err)
				}
				empty, err := agentcontract.CanonicalJSON(agentcontract.JSON{"page": ""})
				if err != nil {
					t.Fatal(err)
				}
				result := agentcontract.JSON{"page": strings.Repeat("x", size-len(empty))}
				raw, err := agentcontract.CanonicalJSON(result)
				if err != nil || len(raw) != size {
					t.Fatal("incorrect boundary fixture", len(raw), err)
				}
				code := ""
				if status != "succeeded" {
					code = "host_action_rejected"
				}
				for range 2 {
					if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, status, result, code); err != nil {
						t.Fatal("valid receipt could not be retained", err)
					}
				}
				saved, err := f.w.command("alice", command.ID)
				if err != nil || saved.Status != status || saved.ErrorCode != code {
					t.Fatal("large receipt changed outcome", saved.Status, saved.ErrorCode, err)
				}
				stored, err := agentcontract.CanonicalJSON(saved.Result)
				if err != nil || !bytes.Equal(stored, raw) {
					t.Fatal("large receipt lost persisted evidence", len(stored), err)
				}
				if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || accepted {
					t.Fatal("retained outcome authorized a repeated action", accepted, err)
				}
			})
		}
	}
}

func TestBrowserLargeReceiptKeepsCompleteFactAndBoundedModelView(t *testing.T) {
	page := strings.Repeat("x", 70<<10)
	previewed := false
	model := &hostModel{decisions: browserDecisions(), hook: func(packet agentcontract.ContextPacket) {
		for _, fact := range packet.Facts {
			if fact.SourceCapability != "ui.command_status" {
				continue
			}
			raw, err := agentcontract.CanonicalJSON(fact)
			if err != nil || len(raw) >= 65536 || bytes.Contains(raw, []byte(page)) {
				t.Error("large evidence escaped its bounded model view", len(raw), err)
			}
			previewed = true
		}
	}}
	f := newWebFixture(t, model, false)
	run := f.run(t)
	command := f.dispatch(t)
	if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
		t.Fatal(accepted, err)
	}
	if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "succeeded", agentcontract.JSON{"page": page}, ""); err != nil {
		t.Fatal(err)
	}
	f.now += 2
	run, err := f.h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || !previewed {
		t.Fatal("large result did not complete through bounded model context", run.Status, previewed, err)
	}
	encoded, err := json.Marshal(run.State["artifact_ids"])
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := json.Unmarshal(encoded, &ids); err != nil {
		t.Fatal(err)
	}
	retained := false
	for _, id := range ids {
		artifact, err := f.h.Store.GetArtifact(run.RunID, id, "alice")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := agentcontract.CanonicalJSON(artifact)
		if err != nil {
			t.Fatal(err)
		}
		retained = retained || bytes.Contains(raw, []byte(page))
	}
	if !retained {
		t.Fatal("bounded model projection replaced the complete persistent fact")
	}
}

func TestBrowserReceiptRejectsInvalidDataWithoutChangingCommand(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "unknown"} {
		for _, payload := range []struct {
			name   string
			result agentcontract.JSON
		}{
			{"undeclared_field", agentcontract.JSON{"page": "orders", "private_token": "synthetic-private-marker"}},
			{"wrong_type", agentcontract.JSON{"page": 42}},
			{"oversized", agentcontract.JSON{"page": strings.Repeat("x", 1<<20)}},
		} {
			t.Run(status+"/"+payload.name, func(t *testing.T) {
				f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
				f.run(t)
				command := f.dispatch(t)
				if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
					t.Fatal(accepted, err)
				}
				code := ""
				if status != "succeeded" {
					code = "host_action_rejected"
				}
				if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, status, payload.result, code); agentcontract.ErrorCode(err) != "browser_result_invalid" {
					t.Errorf("invalid %s receipt accepted: %v", status, err)
				}
				saved, err := f.w.command("alice", command.ID)
				if err != nil || saved.Status != "running" || saved.Result != nil || saved.ErrorCode != "" {
					t.Errorf("rejected receipt changed saved command: status=%s, has_result=%t, error=%v", saved.Status, saved.Result != nil, err)
				}
			})
		}
	}
}

func TestBrowserSucceededReceiptCannotCarryError(t *testing.T) {
	for _, code := range []string{"host_action_rejected", "synthetic secret: raw upstream body"} {
		t.Run(code, func(t *testing.T) {
			f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
			f.run(t)
			command := f.dispatch(t)
			if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
				t.Fatal(accepted, err)
			}
			if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "succeeded", agentcontract.JSON{"page": "orders"}, code); agentcontract.ErrorCode(err) != "browser_result_invalid" {
				t.Errorf("success with error accepted: %v", err)
			}
			saved, err := f.w.command("alice", command.ID)
			if err != nil || saved.Status != "running" || saved.Result != nil || saved.ErrorCode != "" {
				t.Errorf("invalid receipt was persisted: status=%s, has_result=%t, error=%v", saved.Status, saved.Result != nil, err)
			}
		})
	}
}

func TestBrowserNonSuccessReceiptRetainsDeclaredResultAndLateSettlement(t *testing.T) {
	for _, status := range []string{"failed", "unknown"} {
		for _, result := range []agentcontract.JSON{nil, {"page": "orders"}} {
			t.Run(status+"/"+map[bool]string{true: "without_result", false: "declared_result"}[result == nil], func(t *testing.T) {
				f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
				f.run(t)
				command := f.dispatch(t)
				if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
					t.Fatal(accepted, err)
				}
				for range 2 {
					saved, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, status, result, "host_action_rejected")
					if err != nil || saved.Status != status || saved.ErrorCode != "host_action_rejected" || agentcontract.WebHash(saved.Result) != agentcontract.WebHash(result) {
						t.Fatal(saved, err)
					}
				}
				settled, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "succeeded", agentcontract.JSON{"page": "orders"}, "")
				if status == "failed" {
					if agentcontract.ErrorCode(err) != "browser_result_conflict" {
						t.Fatal("definite failure was overwritten", err)
					}
				} else if err != nil || settled.Status != "succeeded" || settled.ErrorCode != "" {
					t.Fatal("original unknown receipt could not settle", settled, err)
				}
			})
		}
	}
}
