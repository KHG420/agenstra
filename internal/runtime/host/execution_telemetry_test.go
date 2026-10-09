package host

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestExecutionTelemetryTracksModelToolAndInputWait(t *testing.T) {
	p := &hostProvider{}
	m := &hostModel{decisions: []agentcontract.Decision{callDecision("records.get"), {Kind: "request_input", Field: "where", Prompt: "where?"}}}
	h := testHost(t, testStore(t), p, m)
	run := createTestHostRun(t, h)
	m.hook = func(agentcontract.ContextPacket) {
		v, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
		if err != nil || v.Execution.Stage != "model_decision" {
			t.Fatalf("%+v %v", v.Execution, err)
		}
	}
	p.hook = func(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		v, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
		if err != nil || v.Execution.Stage != "tool_execution" || len(v.Execution.Active) != 1 {
			t.Fatalf("%+v %v", v.Execution, err)
		}
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"ok": true}}, nil
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	v, callErr := h.GetTelemetry(t.Context(), run.RunID, "alice")
	if callErr != nil {
		t.Error(callErr)
	}
	if v.Execution.Stage != "needs_input" || v.Execution.WaitReason == nil || v.Execution.StartedAt == nil {
		t.Fatalf("%+v", v.Execution)
	}
}

func TestMemoryExtractionTelemetryIsActiveBeforeDecision(t *testing.T) {
	m := &memoryTestModel{hostModel: &hostModel{}}
	h, _ := memoryTestHost(t, m)
	run := createTestHostRun(t, h)
	m.extract = func(agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
		v, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
		if err != nil || v.Execution.Stage != "memory_extraction" || v.Execution.StartedAt == nil || v.Budget.Tokens.ReservedTokens <= 0 || v.Budget.Tokens.UnknownTokens != 0 {
			t.Fatalf("execution=%+v tokens=%+v err=%v", v.Execution, v.Budget.Tokens, err)
		}
		return nil, nil
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || len(m.inputs) != 1 {
		t.Fatalf("run=%s inputs=%v err=%v", run.Status, m.inputs, err)
	}
}
