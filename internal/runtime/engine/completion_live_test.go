package engine

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type completionProposalModel struct {
	*HTTPJSONDecisionModel
	proposal *Decision
}

func (m *completionProposalModel) Decide(ctx context.Context, packet ContextPacket, prompt string) (Decision, error) {
	if m.proposal != nil {
		proposal := *m.proposal
		m.proposal = nil
		return proposal, nil
	}
	return m.HTTPJSONDecisionModel.Decide(ctx, packet, prompt)
}

// Replays the actual contradictory final from an isolated lottery integration.
// The first proposal is injected; only review calls reach the real model. There
// are no business handlers or personal data. Inspect the logged answer for
// semantic correctness: valid citations alone are not a semantic assertion.
func TestLiveWriteCompletionConsistencyReplay(t *testing.T) {
	if os.Getenv("AGENSTRA_LIVE_EVAL") != "1" {
		t.Skip("set AGENSTRA_LIVE_EVAL=1 and AGENT_MODEL credentials to run live evaluation")
	}
	var fixture struct {
		RuntimeState
		Proposal Decision `json:"proposal"`
	}
	raw, err := os.ReadFile("testdata/completion-start-round.json")
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("historical synthetic fixture unavailable", err)
	}
	for i := 0; i < 3; i++ {
		t.Run(string(rune('1'+i)), func(t *testing.T) {
			model, err := NewHTTPJSONDecisionModel(os.Getenv("AGENT_MODEL"), os.Getenv("AGENT_MODEL_BASE_URL"), os.Getenv("AGENT_MODEL_API_KEY"), 60*time.Second, nil)
			if err != nil {
				t.Fatal("live evaluation requires AGENT_MODEL, AGENT_MODEL_BASE_URL and AGENT_MODEL_API_KEY")
			}
			model.APIType, model.Thinking, model.ReasoningEffort = os.Getenv("AGENT_MODEL_API_TYPE"), os.Getenv("AGENT_MODEL_THINKING"), os.Getenv("AGENT_MODEL_REASONING_EFFORT")
			p := &hostProvider{caps: map[string]CapabilityDescription{"ui.start_round": {Name: "ui.start_round", Effect: "write", ApprovalRequired: true}}}
			proposal := fixture.Proposal
			r := &AgentRuntime{Provider: liveEvaluationProvider{p}, Model: &completionProposalModel{HTTPJSONDecisionModel: model, proposal: &proposal}, MaxModelRounds: 4}
			s, callErr := r.NewState(fixture.Instruction, fixture.RunID)
			if callErr != nil {
				t.Error(callErr)
			}
			s.Facts, s.Observations, s.ModelObservations, s.InvocationReceipts = fixture.Facts, fixture.Observations, fixture.ModelObservations, fixture.InvocationReceipts
			started := time.Now()
			if err = r.Step(t.Context(), s, nil); err != nil || s.Status != "completed" || s.AnswerMarkdown == proposal.AnswerMarkdown || p.calls != 0 || len(s.ModelCalls) != 2 || s.ModelCalls[1].Purpose != "completion_review" {
				t.Fatalf("review status=%s tools=%d err=%v", s.Status, p.calls, err)
			}
			t.Logf("gateway_requests=%d input_tokens=%d output_tokens=%d elapsed=%s\nreviewed_answer=%s", s.ModelCalls[1].Attempts, s.ModelCalls[1].InputTokens, s.ModelCalls[1].OutputTokens, time.Since(started), s.AnswerMarkdown)
		})
	}
}

type liveEvaluationProvider struct{ *hostProvider }

func (p liveEvaluationProvider) SystemPrompt() string {
	return AgentPrompt("Use synthetic capabilities to complete the request. Ask for a record id when none is provided.")
}

// These evaluations use synthetic tools and a real model gateway. They never
// invoke business services. Opt-in keeps credentials and paid calls out of CI.
func TestLiveAgentCompletionEvaluation(t *testing.T) {
	if os.Getenv("AGENSTRA_LIVE_EVAL") != "1" {
		t.Skip("set AGENSTRA_LIVE_EVAL=1 and AGENT_MODEL credentials to run live evaluation")
	}
	model, err := NewHTTPJSONDecisionModel(os.Getenv("AGENT_MODEL"), os.Getenv("AGENT_MODEL_BASE_URL"), os.Getenv("AGENT_MODEL_API_KEY"), 30*time.Second, nil)
	if err != nil {
		t.Fatal("live evaluation requires AGENT_MODEL, AGENT_MODEL_BASE_URL and AGENT_MODEL_API_KEY")
	}
	for _, tc := range []struct {
		name, instruction string
		needsTool         bool
	}{
		{"greeting", "Say hello briefly.", false},
		{"evidence", "Look up R-1 and report its current count. Do not invent the count.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &hostProvider{caps: map[string]CapabilityDescription{"records.get": {Name: "records.get", Version: "1", Description: "Return the current count for a record.", Effect: "read", Replay: "safe", InputSchema: JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []string{"id"}}}}}
			p.hook = func(context.Context, string, JSON, *InvocationContext) (CapabilityResult, error) {
				return CapabilityResult{Data: JSON{"id": "R-1", "count": 42}}, nil
			}
			r := &AgentRuntime{Provider: liveEvaluationProvider{p}, Model: model, Grants: map[string]bool{"records.get": true}, MaxModelRounds: 8}
			if tc.needsTool {
				evidence, callErr2 := RequireFactValues(FactRequirement{Capability: "records.get", Path: []any{"data", "count"}, Value: 42})
				if callErr2 != nil {
					t.Error(callErr2)
				}
				r.CompletionValidator = func(ctx context.Context, c CompletionContext) error {
					if err := evidence(ctx, c); err != nil {
						return err
					}
					if !strings.Contains(c.AnswerMarkdown, "42") {
						return CompletionValidationError{"answer_value_mismatch", "Include the verified count 42."}
					}
					return nil
				}
			}
			started := time.Now()
			result, err := r.Run(t.Context(), tc.instruction)
			if err != nil || result.Status != "completed" || (!tc.needsTool && p.calls != 0) || (tc.needsTool && p.calls == 0) {
				t.Fatalf("status=%s tools=%d err=%v", result.Status, p.calls, err)
			}
			t.Logf("case=%s success=true model_decisions=%d tool_calls=%d elapsed=%s", tc.name, len(result.Decisions), p.calls, time.Since(started))
		})
	}
}
