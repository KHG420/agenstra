package capability

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/platform/modelapi"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

type completionProposalModel struct {
	*modelapi.HTTPJSONDecisionModel
	proposal *agentcontract.Decision
}

func (m *completionProposalModel) Decide(ctx context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
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
		agentcontract.RuntimeState
		Proposal agentcontract.Decision `json:"proposal"`
	}
	raw, err := os.ReadFile("testdata/completion-start-round.json")
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("historical synthetic fixture unavailable", err)
	}
	for i := 0; i < 3; i++ {
		t.Run(string(rune('1'+i)), func(t *testing.T) {
			model, err := modelapi.NewHTTPJSONDecisionModel(os.Getenv("AGENT_MODEL"), os.Getenv("AGENT_MODEL_BASE_URL"), os.Getenv("AGENT_MODEL_API_KEY"), 60*time.Second, nil)
			if err != nil {
				t.Fatal("live evaluation requires AGENT_MODEL, AGENT_MODEL_BASE_URL and AGENT_MODEL_API_KEY")
			}
			model.APIType, model.Thinking, model.ReasoningEffort = os.Getenv("AGENT_MODEL_API_TYPE"), os.Getenv("AGENT_MODEL_THINKING"), os.Getenv("AGENT_MODEL_REASONING_EFFORT")
			p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"ui.start_round": {Name: "ui.start_round", Effect: "write", ApprovalRequired: true}}}
			proposal := fixture.Proposal
			r := &reactcore.AgentRuntime{Provider: liveEvaluationProvider{p}, Model: &completionProposalModel{HTTPJSONDecisionModel: model, proposal: &proposal}, MaxModelRounds: 4}
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
