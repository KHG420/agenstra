package agenstra

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type coreTestProvider struct {
	caps   map[string]CapabilityDescription
	called int
}

func (p *coreTestProvider) Capabilities() map[string]CapabilityDescription { return p.caps }
func (p *coreTestProvider) Skills() map[string]Skill                       { return map[string]Skill{} }
func (p *coreTestProvider) SystemPrompt() string                           { return "test" }
func (p *coreTestProvider) Invoke(_ context.Context, _ string, _ map[string]any, _ *InvocationContext) (CapabilityResult, error) {
	p.called++
	return CapabilityResult{Data: JSON{"value": 42}}, nil
}
func (p *coreTestProvider) Close() error { return nil }

type coreTestModel struct {
	decisions []Decision
	errs      []error
	calls     int
}

func (m *coreTestModel) Decide(_ context.Context, _ ContextPacket, _ string) (Decision, error) {
	i := m.calls
	m.calls++
	if i < len(m.errs) && m.errs[i] != nil {
		return Decision{}, m.errs[i]
	}
	return m.decisions[i], nil
}
func TestCoreRuntimeRepairAndRepeat(t *testing.T) {
	cap := CapabilityDescription{Name: "calc.sum", Version: "1", Description: "calculate", InputSchema: JSON{"type": "object"}, Effect: "compute"}
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}
	call := func(ref string) Decision {
		return Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: ref, Capability: cap.Name, Arguments: JSON{"n": 1}, Reason: "calculate"}}}
	}
	model := &coreTestModel{decisions: []Decision{{}, call("first"), call("second"), {Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "done"}}, errs: []error{ModelDecisionError{"model_decision_invalid"}}}
	runtime := &AgentRuntime{Provider: provider, Model: model, MaxModelRounds: 6}
	state, err := runtime.NewState("run calculation", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Step(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.RoundsUsed != 2 || len(state.Pending) != 1 {
		t.Fatalf("repair budget/pending: %+v", state)
	}
	outcome, err := ExecuteCall(context.Background(), provider, map[string]bool{"calc.sum": true}, state.Pending[0].Call, nil)
	if err != nil {
		t.Fatal(err)
	}
	Observe(state, &state.Pending[0], outcome)
	state.Pending = nil
	if err = runtime.Step(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	if len(state.Pending) != 0 || state.Observations[len(state.Observations)-1].ErrorCode == nil || *state.Observations[len(state.Observations)-1].ErrorCode != "repeated_equivalent_call" {
		t.Fatalf("repeat not blocked: %+v", state.Observations)
	}
	if provider.called != 1 {
		t.Fatalf("unexpected provider calls: %d", provider.called)
	}
}
func TestCoreFactReferenceExpiryAndPath(t *testing.T) {
	now := time.Now().Add(-time.Second)
	fact := Fact{FactID: NewID(), Value: JSON{"data": JSON{"items": []any{JSON{"id": "A"}}}}, ReferenceScope: "connection", ConnectionID: strptr("one"), ExpiresAt: &now}
	ref := JSON{"$fact_value": JSON{"fact_id": fact.FactID, "path": []any{"data", "items", 0, "id"}}}
	_, err := ResolveArgument(ref, map[string]Fact{fact.FactID: fact}, "one", true)
	if err == nil || err.Error() != "fact_reference_expired" {
		t.Fatalf("wanted expiry, got %v", err)
	}
	got, err := ResolveArgument(ref, map[string]Fact{fact.FactID: fact}, "one", false)
	if err != nil || got != "A" {
		t.Fatalf("path resolution: %v %v", got, err)
	}
	_, err = ResolveArgument(JSON{"$fact_id": fact.FactID, "x": 1}, map[string]Fact{fact.FactID: fact}, "", false)
	if !errors.Is(err, errors.New("fact_reference_invalid")) && err.Error() != "fact_reference_invalid" {
		t.Fatal(err)
	}
}

func TestCoreFactPreviewArrayLengthAndInspectPrompt(t *testing.T) {
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{}}
	results := []any{}
	for i := 0; i < 40; i++ {
		results = append(results, JSON{"status": "partial", "audit": JSON{"trace": strings.Repeat("x", 12000)}})
	}
	fact := Fact{FactID: NewID(), SourceCapability: "monitor.batch", SourceVersion: "1", Value: JSON{"data": JSON{"results": results}}, Quality: "provider_reported", ObservedAt: time.Now().UTC(), ReferenceScope: "durable"}
	model := &promptCaptureModel{decisions: []Decision{{Schema: "agenstra.decision.v1", Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "results"}}, {Schema: "agenstra.decision.v1", Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "results", 39, "status"}}, {Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Inspected", FactIDs: []string{fact.FactID}}}}
	runtime := &AgentRuntime{Provider: provider, Model: model}
	state, err := runtime.NewState("Count every interval", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = append(state.Facts, fact)
	packet := runtime.Context(state)
	if len(packet.Facts) != 1 {
		t.Fatal(packet)
	}
	found := false
	for _, note := range packet.ContextOmissions {
		if strings.Contains(note, `array at ["data","results"] has 40 items`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing full length note: %v", packet.ContextOmissions)
	}
	for i := 0; i < 3; i++ {
		if err := runtime.Step(context.Background(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	if state.Status != "completed" || !strings.Contains(model.prompts[1], "has 40 items, but its preview shows only 32") || !strings.Contains(model.prompts[2], "Report the total as 40") {
		t.Fatalf("array reminders missing: %q status=%s", model.prompts, state.Status)
	}
}

type promptCaptureModel struct {
	decisions []Decision
	prompts   []string
}

func (m *promptCaptureModel) Decide(_ context.Context, _ ContextPacket, prompt string) (Decision, error) {
	m.prompts = append(m.prompts, prompt)
	return m.decisions[len(m.prompts)-1], nil
}
func TestCoreFiniteJSONAndContextBudget(t *testing.T) {
	if _, err := CanonicalJSON(JSON{"bad": []any{math.NaN()}}); err == nil {
		t.Fatal("NaN accepted")
	}
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{}}
	runtime := &AgentRuntime{Provider: provider, MaxContextCharacters: 3000}
	state, err := runtime.NewState(strings.Repeat("x", 2150), "")
	if err != nil {
		t.Fatal(err)
	}
	items := []any{}
	for i := 0; i < 100; i++ {
		items = append(items, i)
	}
	fact := Fact{FactID: NewID(), Value: JSON{"data": JSON{"results": items}}, ReferenceScope: "durable"}
	state.Facts = []Fact{fact}
	packet := runtime.Context(state)
	raw, err := CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)+len(provider.SystemPrompt()) > 3000 {
		t.Fatalf("context over budget: %d", len(raw))
	}
	if len(packet.Facts) != 1 || len(packet.Facts[0].Value) != 0 {
		t.Fatalf("fallback lost identity or leaked preview: %+v", packet.Facts)
	}
	found := false
	for _, note := range packet.ContextOmissions {
		if strings.Contains(note, "has 100 items") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fallback lost array length: %v", packet.ContextOmissions)
	}
}

func TestCoreChineseContextBudgetCountsCharacters(t *testing.T) {
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{}}
	model := &promptCaptureModel{decisions: []Decision{{Kind: "final", AnswerMarkdown: "完成"}}}
	runtime := &AgentRuntime{Provider: provider, Model: model, MaxContextCharacters: 1600}
	state, err := runtime.NewState(strings.Repeat("航", 1000), "")
	if err != nil {
		t.Fatal(err)
	}
	packet := runtime.Context(state)
	raw, err := CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 1600 || utf8.RuneCount(raw) > 1600 {
		t.Fatalf("test setup did not separate bytes from chars: bytes=%d chars=%d", len(raw), utf8.RuneCount(raw))
	}
	if err := runtime.Step(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "completed" {
		t.Fatalf("Chinese context rejected: status=%s error=%v", state.Status, state.ErrorCode)
	}
}

func TestCoreOversizedBatchGetsSpecificRepair(t *testing.T) {
	calls := []ToolCall{}
	for i := 0; i < 5; i++ {
		calls = append(calls, ToolCall{CallRef: fmt.Sprintf("call-%d", i), Capability: "x.read", Arguments: JSON{}, Reason: "read"})
	}
	model := &promptCaptureModel{decisions: []Decision{{Kind: "tool_batch", Calls: calls}, {Kind: "final", AnswerMarkdown: "corrected"}}}
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: model}
	state, err := runtime.NewState("repair", "")
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	if err := runtime.Step(context.Background(), state, func() error { checkpoints++; return nil }); err != nil {
		t.Fatal(err)
	}
	if state.Status != "completed" || state.RoundsUsed != 2 || checkpoints != 2 || !strings.Contains(model.prompts[1], "at most 4 items") {
		t.Fatalf("repair failed: state=%+v prompts=%q checkpoints=%d", state, model.prompts, checkpoints)
	}
}

func TestCoreTransientBatchApprovalBlocksAllIO(t *testing.T) {
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{"read.first": {Name: "read.first", Version: "1", Effect: "read"}, "write.second": {Name: "write.second", Version: "1", Effect: "write", ApprovalRequired: true}}}
	model := &promptCaptureModel{decisions: []Decision{{Kind: "tool_batch", Calls: []ToolCall{{CallRef: "first", Capability: "read.first", Arguments: JSON{}, Reason: "read"}, {CallRef: "second", Capability: "write.second", Arguments: JSON{}, Reason: "write"}}}}}
	runtime := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"write.second": true}}
	result, err := runtime.Run(context.Background(), "read and write")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "needs_approval" || provider.called != 0 {
		t.Fatalf("approval bypass: status=%s calls=%d", result.Status, provider.called)
	}
}
