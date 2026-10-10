package service

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestBrowserReceiptRejectsInvalidDataWithoutChangingCommand(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "unknown"} {
		for _, payload := range []struct {
			name   string
			result agentcontract.JSON
		}{
			{"undeclared_field", agentcontract.JSON{"page": "orders", "private_token": "synthetic-private-marker"}},
			{"wrong_type", agentcontract.JSON{"page": 42}},
			{"oversized", agentcontract.JSON{"page": strings.Repeat("x", 65536)}},
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
