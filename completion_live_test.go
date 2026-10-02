package agenstra

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

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
				evidence, _ := RequireFactValues(FactRequirement{Capability: "records.get", Path: []any{"data", "count"}, Value: 42})
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
