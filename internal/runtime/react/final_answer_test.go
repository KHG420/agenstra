package react

import (
	"context"
	"slices"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestStructuredFinalCorrectsWithinOriginalRun(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.create", Version: "1", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"}}
	schema, err := agentcontract.CompileFinalAnswerSchema(agentcontract.JSON{"type": "object", "additionalProperties": false, "required": []string{"id"}, "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, first, second, wantStatus string
		calls, retries                  int
	}{
		{"local repair", "Answer:\n```JSON\n{\"id\":\"kept\"\n```", "", "completed", 3, 0},
		{"schema retry", `{"id":"kept","extra":true}`, `{"id":"kept"}`, "completed", 4, 1},
		{"bounded failure", `{"id":"kept","extra":true}`, `{"extra":true}`, "failed", 3, 1},
		{"no tool replay", `{"id":"kept","extra":true}`, "tool", "failed", 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
			calls := 0
			model := decisionModelFunc(func(ctx context.Context, p agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
				calls++
				if !strings.Contains(prompt, "Final answer schema:") {
					t.Fatal("missing final answer schema")
				}
				agentcontract.ObserveAnswer(ctx, agentcontract.AnswerUpdate{Text: "unvalidated draft", Phase: "generating"})
				action := agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "create", Capability: cap.Name, Arguments: agentcontract.JSON{}, Reason: "create the record"}}}
				if calls == 1 {
					return action, nil
				}
				if len(p.Facts) != 1 || len(p.ActionOutcomes) != 1 {
					t.Fatal("lost write receipt")
				}
				answer := tc.first
				if calls >= 3 {
					answer = `{"id":"kept"}`
					if p.CompletionReview == nil {
						if !slices.Contains(p.RuntimeFeatures, agentcontract.FinalAnswerCorrection) || p.ToolCallsRemaining != 0 {
							t.Fatal("correction did not restrict the next decision to the retained result")
						}
						if !strings.Contains(prompt, "final.answer_markdown was rejected") || !strings.Contains(prompt, "extra") {
							t.Fatal("missing field-specific recovery feedback")
						}
						if tc.second == "tool" {
							action.Calls[0].CallRef = "second"
							return action, nil
						}
						answer = tc.second
					}
				}
				return agentcontract.Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: []string{p.Facts[0].FactID}}, nil
			})
			r := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{cap.Name: true}, FinalAnswerSchema: schema}
			ctx := agentcontract.WithAnswerObserver(t.Context(), func(u agentcontract.AnswerUpdate) {
				if u.Text == "unvalidated draft" {
					t.Fatal("invalid draft reached stream")
				}
			})
			s, err := r.Run(ctx, "create a record")
			if err != nil || s.Status != tc.wantStatus || calls != tc.calls || provider.called != 1 || s.ModelUsage.FormatRecoveryRequests != tc.retries {
				t.Fatalf("status %s, calls %d, writes %d, retries %d, error %v", s.Status, calls, provider.called, s.ModelUsage.FormatRecoveryRequests, err)
			}
			if s.Status == "completed" && s.AnswerMarkdown != `{"id":"kept"}` || s.Status == "failed" && s.AnswerMarkdown != "" {
				t.Fatalf("published %q", s.AnswerMarkdown)
			}
		})
	}
}

func TestFirstAnswerSchemaErrorAfterProtocolRecovery(t *testing.T) {
	schema, err := agentcontract.CompileFinalAnswerSchema(agentcontract.JSON{"type": "object", "required": []string{"id"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		rounds    int
		corrected bool
		want      string
		calls     int
	}{
		{"distinct correction", 8, true, "completed", 4},
		{"one inner retry", 8, false, "failed", 4},
		{"round budget", 3, true, "failed", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			model := decisionModelFunc(func(_ context.Context, _ agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
				count++
				if count <= 2 {
					return agentcontract.Decision{ModelCall: &agentcontract.ModelCallMetrics{Attempts: 1, FormatError: "model_output_tool_calls_invalid"}}, agentcontract.ModelDecisionError{Kind: "model_decision_invalid"}
				}
				answer := "{}"
				if count == 4 {
					if !strings.Contains(prompt, "final.answer_markdown was rejected") || !strings.Contains(prompt, "id") {
						t.Fatal("first inner error was not returned to the model")
					}
					if tc.corrected {
						answer = `{"id":"verified"}`
					}
				}
				return agentcontract.Decision{Kind: "final", AnswerMarkdown: answer}, nil
			})
			r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelRounds: tc.rounds, FinalAnswerSchema: schema}
			s, err := r.Run(t.Context(), "give the record ID")
			if err != nil || s.Status != tc.want || count != tc.calls {
				t.Fatalf("status %s, calls %d, error %v", s.Status, count, err)
			}
		})
	}
}
