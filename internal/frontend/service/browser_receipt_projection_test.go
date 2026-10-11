package service

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactruntime "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestBrowserReceiptProjectionRetainsExecutionEvidenceAndBusinessFields(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			model := &hostModel{decisions: browserDecisions()}
			model.hook = func(packet agentcontract.ContextPacket) {
				for _, fact := range packet.Facts {
					if fact.SourceCapability == "ui.navigate" || fact.SourceCapability == "ui.command_status" {
						data, _ := fact.Value["data"].(map[string]any)
						if _, exists := data["command_id"]; exists {
							t.Fatal("framework command ID entered model-visible receipt", data)
						}
					}
				}
			}
			f := newWebFixture(t, model, false)
			profile := frontendTestProfile(false)
			profile.Actions[0].OutputSchema = agentcontract.JSON{
				"type": "object", "additionalProperties": false,
				"properties": agentcontract.JSON{
					"command_id": agentcontract.JSON{"type": "string"},
					"status":     agentcontract.JSON{"type": "string"},
					"data":       agentcontract.JSON{"type": "null"},
					"enabled":    agentcontract.JSON{"type": "boolean"},
					"count":      agentcontract.JSON{"type": "integer"},
				},
				"required": []any{"command_id", "status", "data", "enabled", "count"},
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
			if run.Status != "waiting" {
				t.Fatal(run.Status)
			}
			command := f.dispatch(t)
			if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
				t.Fatal(accepted, err)
			}
			business := agentcontract.JSON{"command_id": "c418e08a-4e38-4e8b-9b5f-c8894c6c093c", "status": "business-state", "data": nil, "enabled": false, "count": 0}
			var result agentcontract.JSON
			code := "handler_rejected"
			if status == "succeeded" {
				result, code = business, ""
			} else if status == "unknown" {
				code = "handler_outcome_unknown"
			}
			if _, err = f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, status, result, code); err != nil {
				t.Fatal(err)
			}
			f.now += 2
			run, err = f.h.Drive(t.Context(), run.RunID, "alice")
			if err != nil {
				t.Fatal(err)
			}
			wantedRunStatus := "completed"
			if status == "unknown" {
				wantedRunStatus = "needs_reconciliation"
			}
			if run.Status != wantedRunStatus {
				t.Fatal("operation outcome changed", run.Status, wantedRunStatus)
			}
			state, err := f.h.Restore(run)
			if err != nil {
				t.Fatal(err)
			}
			fact := state.Facts[len(state.Facts)-1]
			if fact.SourceCapability != "ui.command_status" {
				t.Fatal("automatic polling lost its command ID", fact.SourceCapability)
			}
			full := fact.Value["data"].(map[string]any)
			if full["command_id"] != command.ID || full["status"] != status {
				t.Fatal("full persisted receipt changed", full)
			}
			visible := agentcontract.ModelFactValue(fact)["data"].(map[string]any)
			if _, exists := visible["command_id"]; exists || visible["status"] != status {
				t.Fatal("receipt projection changed execution status or exposed correlation", visible)
			}
			facts := map[string]agentcontract.Fact{fact.FactID: fact}
			ref := func(path ...any) agentcontract.JSON {
				return agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": path}}
			}
			if _, err = reactruntime.ResolveArgument(ref("data", "command_id"), facts, "", false); err == nil || err.Error() != "fact_reference_path_invalid" {
				t.Fatal("inspection/reference exposed the internal ID", err)
			}
			root, err := reactruntime.ResolveArgument(ref("data"), facts, "", false)
			if err != nil || agentcontract.WebHash(root) != agentcontract.WebHash(visible) {
				t.Fatal("root inspection bypassed projection", root, err)
			}
			if status == "succeeded" {
				if agentcontract.WebHash(visible["result"]) != agentcontract.WebHash(business) {
					t.Fatal("business output was filtered or flattened", visible)
				}
				id, err := reactruntime.ResolveArgument(ref("data", "result", "command_id"), facts, "", false)
				if err != nil || id != business["command_id"] {
					t.Fatal("legitimate business UUID hidden", id, err)
				}
			} else if visible["error_code"] != code {
				t.Fatal("failure or unknown reason lost", visible)
			}
			artifact, err := f.h.Store.GetArtifact(run.RunID, fact.FactID, "alice")
			if err != nil || agentcontract.WebHash(artifact) != agentcontract.WebHash(fact) {
				t.Fatal("full artifact was not retained", err)
			}
			var count int
			if err = f.w.Store.store.DB.QueryRow("SELECT count(*) FROM web_commands").Scan(&count); err != nil || count != 1 {
				t.Fatal("projection caused command replay", count, err)
			}
		})
	}
}
