package service

import (
	"database/sql"
	"encoding/json"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestBrowserNumericArgumentsPreserveTheirDecimalValue(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
		valid bool
	}{
		{"integer", 42, true},
		{"decimal", json.Number("0.1"), true},
		{"decimal notation", json.Number("42.00"), true},
		{"scientific notation", json.Number("4.2e1"), true},
		{"exact large number", json.Number("1e20"), true},
		{"exact binary64 boundary", json.Number("9007199254740992"), true},
		{"rounded boundary", json.Number("9007199254740993"), false},
		{"rounded int64", int64(9223372036854775807), false},
		{"rounded negative", json.Number("-9007199254740993"), false},
		{"rounded decimal", json.Number("0.10000000000000001"), false},
		{"underflow", json.Number("1e-9999"), false},
		{"zero exponent", json.Number("0e-9999"), true},
		{"negative zero", json.Number("-0.0"), true},
		{"subnormal round trip", json.Number("5e-324"), true},
		{"overflow", json.Number("1e309"), false},
		{"string identifier", "9007199254740993", true},
		{"nested array", []any{agentcontract.JSON{"id": json.Number("9007199254740993")}}, false},
		{"nested exact", []any{agentcontract.JSON{"id": "9007199254740993", "ratio": 0.1}}, true},
		{"null", nil, true},
		{"boolean", false, true},
		{"invalid JSON value", make(chan int), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := browserNumbersRoundTrip(agentcontract.JSON{"value": tt.value}); got != tt.valid {
				t.Fatalf("numeric transport validity = %t, want %t", got, tt.valid)
			}
		})
	}
}

func TestBrowserCommandRejectsNumbersThatJavaScriptWouldRound(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	profile := frontendTestProfile(false)
	profile.Actions[0].InputSchema = agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
		"record_id": agentcontract.JSON{"type": "integer"},
	}, "required": []any{"record_id"}, "additionalProperties": false}
	compiled, err := compileFrontend(profile)
	if err != nil {
		t.Fatal(err)
	}
	f.w.profiles["records-web"] = compiled
	session, key, err := f.w.CreateBrowserSession(t.Context(), "alice", "records-web", "1", []string{"ui.navigate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.UpdatePageObservation("alice", session.ID, key, 1, 0, agentcontract.JSON{"page": "home"}); err != nil {
		t.Fatal(err)
	}
	run, err := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", session.ID, "Update the exact record", "number-safety")
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
	result, err := provider.Invoke(t.Context(), "ui.navigate", agentcontract.JSON{"record_id": json.Number("9007199254740993")}, inv)
	if err != nil || result.ErrorCode != "browser_argument_number_unsupported" {
		t.Fatalf("an exact input ID was allowed to become a different JavaScript ID: result=%+v error=%v", result, err)
	}
	var commands int
	if err := f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_commands").Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if commands != 0 {
		t.Fatalf("unsafe command persisted: %d", commands)
	}
}

func TestBrowserOldNumericCommandCannotBeginOrLoseAnExistingOutcome(t *testing.T) {
	for _, status := range []string{"dispatched", "running", "unknown", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			f := newWebFixture(t, &hostModel{decisions: browserDecisions()}, false)
			profile := frontendTestProfile(false)
			profile.Actions[0].InputSchema["properties"] = agentcontract.JSON{
				"page": agentcontract.JSON{"anyOf": []any{agentcontract.JSON{"type": "string"}, agentcontract.JSON{"type": "integer"}}},
			}
			compiled, err := compileFrontend(profile)
			if err != nil {
				t.Fatal(err)
			}
			f.w.profiles["records-web"] = compiled
			f.session, f.key, err = f.w.CreateBrowserSession(t.Context(), "alice", "records-web", "1", []string{"ui.navigate"})
			if err != nil {
				t.Fatal(err)
			}
			f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, 0, agentcontract.JSON{"page": "home"})
			if err != nil {
				t.Fatal(err)
			}
			run := f.run(t)
			command := f.dispatch(t)
			arguments := agentcontract.JSON{"page": json.Number("9007199254740993")}
			if err := agentcontract.ValidateSchema(compiled.inputs[command.Action], arguments); err != nil {
				t.Fatal("historical arguments must satisfy the original declared schema", err)
			}
			// Reconstruct a checkpoint and command created before the numeric
			// guard. Their exact arguments still agree, so authorization remains
			// the real production check rather than a test bypass.
			var raw string
			if err := f.h.Store.DB.QueryRow("SELECT state_json FROM runs WHERE run_id=?", run.RunID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var snapshot agentcontract.JSON
			if err := agentcontract.DecodeDocument(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			runtime := snapshot["runtime"].(map[string]any)
			found := false
			for _, entry := range runtime["pending"].([]any) {
				item := entry.(map[string]any)
				if item["invocation_id"] != command.ID {
					continue
				}
				call := item["call"].(map[string]any)
				call["arguments"] = arguments
				item["arguments_sha256"] = reactcore.ArgumentsDigest(agentcontract.ToolCall{Capability: command.Action, Arguments: arguments})
				found = true
			}
			if !found {
				t.Fatal("original invocation checkpoint missing")
			}
			updated, err := agentcontract.EncodeDocument(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.Store.DB.Exec("UPDATE runs SET state_json=? WHERE run_id=?", updated, run.RunID); err != nil {
				t.Fatal(err)
			}
			command.Arguments = arguments
			command.ArgumentsSHA256 = agentcontract.WebHash(arguments)
			command.Status = status
			if status == "succeeded" {
				command.Result = agentcontract.JSON{"page": "orders"}
			}
			if err := f.w.Store.store.Transaction(func(tx *sql.Tx) error {
				return webSave(tx, "web_commands", command.ID, command)
			}); err != nil {
				t.Fatal(err)
			}
			accepted, actual, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, f.session.Generation)
			if err != nil || accepted {
				t.Fatal("unsafe historical command began execution", actual, err)
			}
			if status == "dispatched" {
				if actual.Status != "failed" || actual.ErrorCode != "browser_argument_number_unsupported" {
					t.Fatal("unstarted command was not rejected", actual)
				}
			} else if agentcontract.WebHash(actual) != agentcontract.WebHash(command) {
				t.Fatal("existing execution outcome was changed", actual)
			}
		})
	}
}
