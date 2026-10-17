package react

import (
	"context"
	"encoding/json"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestJSONAnswerRetainsPublicationGuardsAndExistingResults(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		checks     int
	}{
		{"missing citation", "final_fact_citations_invalid", 0},
		{"unknown citation", "final_fact_citations_invalid", 0},
		{"internal identifier", "final_internal_fact_id_exposed", 0},
		{"invalid result reference", "final_result_refs_invalid", 0},
		{"host rejection", "completion_answer_mismatch", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := agentcontract.NewID()
			const businessID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
			const answer = `{"id":"` + businessID + `","count":0,"missing":null}`
			provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
			calls, checks := 0, 0
			runtime := &AgentRuntime{Provider: provider}
			runtime.CompletionValidator = func(_ context.Context, completion agentcontract.CompletionContext) error {
				checks++
				if len(completion.Facts) != 1 || len(completion.FactIDs) != 1 || completion.FactIDs[0] != id || completion.AnswerMarkdown != answer {
					t.Fatal("native JSON answer bypassed existing completion evidence", completion)
				}
				if tc.name == "host rejection" && checks == 1 {
					return agentcontract.CompletionValidationError{Kind: tc.code, Feedback: "Recheck the requested values against saved evidence."}
				}
				return nil
			}
			runtime.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				calls++
				value := answer
				citation := id
				path := "id"
				if calls == 1 {
					switch tc.name {
					case "missing citation":
						citation = ""
					case "unknown citation":
						citation = agentcontract.NewID()
					case "internal identifier":
						value = `{"internal":"` + id + `"}`
					case "invalid result reference":
						path = "absent"
					}
				} else if len(packet.Observations) != 1 || packet.Observations[0].ErrorCode == nil || *packet.Observations[0].ErrorCode != tc.code {
					t.Fatal("native JSON answer lost publication correction feedback")
				}
				citations := []string{}
				if citation != "" {
					citations = append(citations, citation)
				}
				fields := agentcontract.JSON{"kind": "final", "answer_json": json.RawMessage(value), "fact_ids": citations}
				if citation == id {
					fields["result_refs"] = []any{agentcontract.JSON{"fact_id": id, "path": []string{"data", path}, "entity_type": "record"}}
				}
				raw, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				return agentcontract.StrictDecision(raw)
			})
			state, err := runtime.NewState("Report the business record as raw JSON", "")
			if err != nil {
				t.Fatal(err)
			}
			state.Facts = []agentcontract.Fact{{FactID: id, SourceCapability: "records.read", Value: agentcontract.JSON{"data": agentcontract.JSON{"id": businessID}}, ReferenceScope: "durable"}}
			if err := runtime.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			if state.Status == "completed" || state.AnswerMarkdown != "" || checks != tc.checks || len(state.Observations) != 1 || state.Observations[0].ErrorCode == nil || *state.Observations[0].ErrorCode != tc.code {
				t.Fatal("native JSON answer bypassed publication guard", state.Status, state.Observations, checks)
			}
			if err := runtime.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			result := runtime.Result(state)
			if state.Status != "completed" || result.AnswerMarkdown != answer || checks != tc.checks+1 || len(result.ResultRefs) != 1 || result.ResultRefs[0].ID != businessID || provider.called != 0 || state.ToolCallsUsed != 0 || state.RoundsUsed != 2 || calls != 2 {
				t.Fatal("native JSON correction changed results, executed tools or bypassed budgets", result, checks, calls)
			}
		})
	}
}
