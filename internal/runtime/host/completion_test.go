package host

import (
	"context"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type decisionModelFunc func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error)

func (f decisionModelFunc) Decide(ctx context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
	return f(ctx, packet, prompt)
}

func TestHostCompletionValidatorUsesCompleteEvidence(t *testing.T) {
	p := &hostProvider{}
	p.hook = func(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "R-1", "detail": strings.Repeat("x", 9000)}}, nil
	}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision("records.get")}})
	h.CompletionValidator = func(ctx context.Context, result agentcontract.CompletionContext) error {
		if result.OriginPackID != "records" || len(result.Facts[0].Value["data"].(agentcontract.JSON)["detail"].(string)) != 9000 {
			t.Fatal("validator received a preview")
		}
		return nil
	}
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatalf("%+v %v", run, err)
	}
}
