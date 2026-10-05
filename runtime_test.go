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

func TestInputValidationFeedbackIdentifiesMissingWrapper(t *testing.T) {
	cap := CapabilityDescription{Name: "records.create", Effect: "write", InputSchema: JSON{"type": "object", "properties": JSON{"arguments": JSON{"type": "object"}}, "required": []any{"arguments"}, "additionalProperties": false}}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}, Grants: map[string]bool{cap.Name: true}}
	s := &RuntimeState{RunID: "run", Status: "queued", Instruction: "Create a record"}
	obs := Observation{CallRef: "create-1", Capability: cap.Name, Status: "failed", ErrorCode: strptr("capability_input_invalid"), Arguments: JSON{"request": JSON{}}}
	s.Observations, s.ModelObservations = []Observation{obs}, []Observation{obs}
	r.Model = decisionModelFunc(func(_ context.Context, _ ContextPacket, prompt string) (Decision, error) {
		if !strings.Contains(prompt, "missing property 'arguments'") || !strings.Contains(prompt, "records.create") || strings.Contains(prompt, "file://") {
			t.Fatal("model did not receive the actual input validation error", prompt)
		}
		return Decision{Kind: "inspect_capability", Name: cap.Name}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeReducesUncodedModelErrorsToSafeCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"uncoded", errors.New("private endpoint and token details"), "model_unavailable"},
		{"local measurement code", errors.New("model_context_measurement_failed"), "model_context_measurement_failed"},
		{"wrapped code", fmt.Errorf("private endpoint: %w", ModelDecisionError{"model_refused"}), "model_refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &coreTestModel{errs: []error{tc.err}}
			runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: model}
			result, err := runtime.Run(t.Context(), "run task")
			if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != tc.want {
				t.Fatalf("model failure: %+v, %v", result, err)
			}
			if len(result.ModelCalls) != 1 || result.ModelCalls[0].ErrorCode == nil || *result.ModelCalls[0].ErrorCode != tc.want {
				t.Fatalf("model telemetry leaked uncoded error: %+v", result.ModelCalls)
			}
		})
	}
}

type failedMeasurementModel struct{ *coreTestModel }

func (failedMeasurementModel) MeasureInput(ContextPacket, string) (InputMeasurement, error) {
	return InputMeasurement{}, errors.New("private tokenizer endpoint and token details")
}

func TestRuntimeMeasurementErrorsStayPrivate(t *testing.T) {
	model := failedMeasurementModel{&coreTestModel{}}
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: model, MaxModelInputTokens: 1000}
	result, err := runtime.Run(t.Context(), "run task")
	if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != "model_unavailable" || model.calls != 0 {
		t.Fatalf("measurement failure: %+v, %v, model calls=%d", result, err, model.calls)
	}
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

func TestCoreFactPreviewDoesNotInventNullOrEmptyValues(t *testing.T) {
	full := JSON{
		"actual_null":  nil,
		"empty_array":  []any{},
		"empty_object": JSON{},
		"timestamp":    strings.Repeat("2026-08-26T06:00:00Z", 100),
		"weather":      JSON{"source_utc": strings.Repeat("2026-08-26T06:00:00Z", 100)},
	}
	fact := Fact{FactID: NewID(), Value: full}
	view := factView(fact, 160)
	if value, exists := view.Value["actual_null"]; !exists || value != nil {
		t.Fatal("an actual provider null must remain visible")
	}
	for _, key := range []string{"empty_array", "empty_object"} {
		if _, exists := view.Value[key]; !exists {
			t.Fatalf("actual empty value %q was omitted", key)
		}
	}
	for _, key := range []string{"timestamp", "weather"} {
		if value, exists := view.Value[key]; exists {
			t.Fatalf("omitted %q appears as provider data: %#v", key, value)
		}
	}
	if len(view.OmittedPaths) == 0 {
		t.Fatal("missing omission metadata")
	}
	value, err := ResolveArgument(JSON{"$fact_value": JSON{"fact_id": fact.FactID, "path": []any{"weather", "source_utc"}}}, map[string]Fact{fact.FactID: fact}, "", false)
	if err != nil || value != full["weather"].(map[string]any)["source_utc"] {
		t.Fatalf("full stored value was changed: %v %v", value, err)
	}
	array := factView(Fact{Value: JSON{"notes": []any{strings.Repeat("HY model note", 100), nil, JSON{}}}}, 80)
	items := array.Value["notes"].([]any)
	if len(items) != 3 || items[0] != nil || items[1] != nil || len(items[2].(map[string]any)) != 0 {
		t.Fatalf("array indices or actual null/empty values changed: %v", items)
	}
	omitted, callErr := CanonicalJSON(array.OmittedPaths)
	if callErr != nil {
		t.Error(callErr)
	}
	if !strings.Contains(string(omitted), `["notes",0]`) {
		t.Fatalf("array placeholder lost its omission path: %s", omitted)
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
	if !strings.Contains(strings.Join(packet.ContextOmissions, " "), "null placeholders") {
		t.Fatalf("omitted array values need an explicit unknown-value reminder: %v", packet.ContextOmissions)
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
func TestRuntimeProtocolPromptDoesNotChangeProviderPrompt(t *testing.T) {
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{}}
	original := provider.SystemPrompt()
	runtime := &AgentRuntime{Provider: provider}
	prompt := runtime.systemPrompt()
	for _, field := range []string{"input_schema", "result_refs", "model_output"} {
		if !strings.Contains(prompt, field) {
			t.Fatalf("runtime prompt omits %s", field)
		}
	}
	if strings.Contains(prompt, "search_capabilities") {
		t.Fatal("disabled capability search advertised")
	}
	runtime.MaxContextCapabilities = 5
	if !strings.Contains(runtime.systemPrompt(), "search_capabilities") {
		t.Fatal("enabled capability search not advertised")
	}
	if provider.SystemPrompt() != original {
		t.Fatal("runtime protocol changed provider prompt")
	}
}
func TestCoreFiniteJSONAndContextBudget(t *testing.T) {
	if _, err := CanonicalJSON(JSON{"bad": []any{math.NaN()}}); err == nil {
		t.Fatal("NaN accepted")
	}
	provider := &coreTestProvider{caps: map[string]CapabilityDescription{}}
	// Keep the packet allowance constant while accounting for runtime guidance.
	runtime := &AgentRuntime{Provider: provider}
	budget := 3000 + utf8.RuneCountInString(runtime.systemPrompt())
	runtime.MaxContextCharacters = budget
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
	if len(raw)+len(runtime.systemPrompt()) > budget {
		t.Fatalf("context over budget: %d", len(raw))
	}
	if len(packet.Facts) != 1 || packet.Facts[0].FactID != fact.FactID || len(packet.Facts[0].OmittedPaths) == 0 {
		t.Fatalf("budgeting lost identity or incomplete-preview markers: %+v", packet.Facts)
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
	runtime := &AgentRuntime{Provider: provider, Model: model}
	runtime.MaxContextCharacters = 1600 + utf8.RuneCountInString(runtime.systemPrompt())
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
