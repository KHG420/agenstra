package react

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type coreTestProvider struct {
	caps   map[string]agentcontract.CapabilityDescription
	called int
}

func (p *coreTestProvider) Capabilities() map[string]agentcontract.CapabilityDescription {
	return p.caps
}

func (p *coreTestProvider) Skills() map[string]agentcontract.Skill {
	return map[string]agentcontract.Skill{}
}

func (p *coreTestProvider) SystemPrompt() string { return "test" }

func (p *coreTestProvider) Invoke(_ context.Context, _ string, _ map[string]any, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
	p.called++
	return agentcontract.CapabilityResult{Data: agentcontract.JSON{"value": 42}}, nil
}

func (p *coreTestProvider) Close() error { return nil }

type coreTestModel struct {
	decisions []agentcontract.Decision
	errs      []error
	calls     int
}

func (m *coreTestModel) Decide(_ context.Context, _ agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
	i := m.calls
	m.calls++
	if i < len(m.errs) && m.errs[i] != nil {
		return agentcontract.Decision{}, m.errs[i]
	}
	return m.decisions[i], nil
}

func TestInputValidationFeedbackIdentifiesMissingWrapper(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.create", Effect: "write", InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"arguments": agentcontract.JSON{"type": "object"}}, "required": []any{"arguments"}, "additionalProperties": false}}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, Grants: map[string]bool{cap.Name: true}}
	s := &agentcontract.RuntimeState{RunID: "run", Status: "queued", Instruction: "Create a record"}
	obs := agentcontract.Observation{CallRef: "create-1", Capability: cap.Name, Status: "failed", ErrorCode: agentcontract.Strptr("capability_input_invalid"), Arguments: agentcontract.JSON{"request": agentcontract.JSON{}}}
	s.Observations, s.ModelObservations = []agentcontract.Observation{obs}, []agentcontract.Observation{obs}
	r.Model = decisionModelFunc(func(_ context.Context, _ agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		if !strings.Contains(prompt, "missing property 'arguments'") || !strings.Contains(prompt, "records.create") || strings.Contains(prompt, "file://") {
			t.Fatal("model did not receive the actual input validation error", prompt)
		}
		return agentcontract.Decision{Kind: "inspect_capability", Name: cap.Name}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
}

func TestInputValidationFeedbackOffersLiteralTagsWithoutRewritingCall(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.manage", Effect: "write", InputSchema: taggedInputSchema("anyOf")}
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
	r := &AgentRuntime{Provider: provider, Grants: map[string]bool{cap.Name: true}}
	s := &agentcontract.RuntimeState{RunID: "run", Status: "queued", Instruction: "Update a record"}
	args := agentcontract.JSON{"kind": "records.update", "arguments": agentcontract.JSON{"id": 1}}
	obs := agentcontract.Observation{CallRef: "update-1", Capability: cap.Name, Status: "failed", ErrorCode: agentcontract.Strptr("capability_input_invalid"), Arguments: args}
	s.Observations, s.ModelObservations = []agentcontract.Observation{obs}, []agentcontract.Observation{obs}
	r.Model = decisionModelFunc(func(_ context.Context, _ agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		if !strings.Contains(prompt, `at "/kind"`) || !strings.Contains(prompt, `allowed literal values: "list", "update"`) ||
			!strings.Contains(prompt, "matching arguments schema") {
			t.Fatal("model did not receive actionable literal tag feedback", prompt)
		}
		return agentcontract.Decision{Kind: "inspect_capability", Name: cap.Name}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if provider.called != 0 || len(s.Pending) != 0 || s.Observations[0].Arguments["kind"] != "records.update" || args["kind"] != "records.update" {
		t.Fatal("diagnostic rewrote or executed the rejected call", s)
	}
}

func TestUnknownCapabilityFeedbackKeepsRuntimeDecisionsSeparate(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.list", Effect: "read"}
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap, "private.delete": {Name: "private.delete", Effect: "destructive"}}}
	r := &AgentRuntime{Provider: provider, Grants: map[string]bool{cap.Name: true}, MaxContextCapabilities: 1}
	s := &agentcontract.RuntimeState{RunID: "run", Status: "queued", Instruction: "Read the records"}
	Reject(s, "search-1", "search_capabilities", "capability_unknown", agentcontract.JSON{"query": "records"}, "")
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		if !strings.Contains(prompt, `Your previous call to "search_capabilities" used an unavailable capability name`) || !strings.Contains(prompt, "standalone decision kinds") || !strings.Contains(prompt, "current authorized capability catalog") {
			t.Fatal("unknown capability did not receive corrective feedback", prompt)
		}
		for _, visible := range packet.Capabilities {
			if visible["name"] == "private.delete" {
				t.Fatal("feedback exposed an unauthorized capability")
			}
		}
		return agentcontract.Decision{Kind: "search_capabilities", Query: "records"}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if provider.called != 0 || len(s.Pending) != 0 || len(s.CapabilitySearchResults) != 1 || s.CapabilitySearchResults[0] != cap.Name {
		t.Fatal("correction dispatched a business call or lost the authorized search", s)
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
		{"wrapped code", fmt.Errorf("private endpoint: %w", agentcontract.ModelDecisionError{Kind: "model_refused"}), "model_refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &coreTestModel{errs: []error{tc.err}}
			runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model}
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

func (failedMeasurementModel) MeasureInput(agentcontract.ContextPacket, string) (agentcontract.InputMeasurement, error) {
	return agentcontract.InputMeasurement{}, errors.New("private tokenizer endpoint and token details")
}

func TestRuntimeMeasurementErrorsStayPrivate(t *testing.T) {
	model := failedMeasurementModel{&coreTestModel{}}
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelInputTokens: 1000}
	result, err := runtime.Run(t.Context(), "run task")
	if err != nil || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != "model_unavailable" || model.calls != 0 {
		t.Fatalf("measurement failure: %+v, %v, model calls=%d", result, err, model.calls)
	}
}

func TestCoreRuntimeRepairAndRepeat(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "calc.sum", Version: "1", Description: "calculate", InputSchema: agentcontract.JSON{"type": "object"}, Effect: "compute"}
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
	call := func(ref string) agentcontract.Decision {
		return agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: ref, Capability: cap.Name, Arguments: agentcontract.JSON{"n": 1}, Reason: "calculate"}}}
	}
	model := &coreTestModel{decisions: []agentcontract.Decision{{}, call("first"), call("second"), {Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "done"}}, errs: []error{agentcontract.ModelDecisionError{Kind: "model_decision_invalid"}}}
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
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"items": []any{agentcontract.JSON{"id": "A"}}}}, ReferenceScope: "connection", ConnectionID: agentcontract.Strptr("one"), ExpiresAt: &now}
	ref := agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"data", "items", 0, "id"}}}
	_, err := ResolveArgument(ref, map[string]agentcontract.Fact{fact.FactID: fact}, "one", true)
	if err == nil || err.Error() != "fact_reference_expired" {
		t.Fatalf("wanted expiry, got %v", err)
	}
	got, err := ResolveArgument(ref, map[string]agentcontract.Fact{fact.FactID: fact}, "one", false)
	if err != nil || got != "A" {
		t.Fatalf("path resolution: %v %v", got, err)
	}
	_, err = ResolveArgument(agentcontract.JSON{"$fact_id": fact.FactID, "x": 1}, map[string]agentcontract.Fact{fact.FactID: fact}, "", false)
	if !errors.Is(err, errors.New("fact_reference_invalid")) && err.Error() != "fact_reference_invalid" {
		t.Fatal(err)
	}
}

func TestCoreFactPreviewDoesNotInventNullOrEmptyValues(t *testing.T) {
	full := agentcontract.JSON{
		"actual_null":  nil,
		"empty_array":  []any{},
		"empty_object": agentcontract.JSON{},
		"timestamp":    strings.Repeat("2026-08-26T06:00:00Z", 100),
		"weather":      agentcontract.JSON{"source_utc": strings.Repeat("2026-08-26T06:00:00Z", 100)},
	}
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), Value: full}
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
	value, err := ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"weather", "source_utc"}}}, map[string]agentcontract.Fact{fact.FactID: fact}, "", false)
	if err != nil || value != full["weather"].(map[string]any)["source_utc"] {
		t.Fatalf("full stored value was changed: %v %v", value, err)
	}
	array := factView(agentcontract.Fact{Value: agentcontract.JSON{"notes": []any{strings.Repeat("HY model note", 100), nil, agentcontract.JSON{}}}}, 80)
	items := array.Value["notes"].([]any)
	if len(items) != 3 || items[0] != nil || items[1] != nil || len(items[2].(map[string]any)) != 0 {
		t.Fatalf("array indices or actual null/empty values changed: %v", items)
	}
	omitted, callErr := agentcontract.CanonicalJSON(array.OmittedPaths)
	if callErr != nil {
		t.Error(callErr)
	}
	if !strings.Contains(string(omitted), `["notes",0]`) {
		t.Fatalf("array placeholder lost its omission path: %s", omitted)
	}
}

func TestCoreFactPreviewArrayLengthAndInspectPrompt(t *testing.T) {
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
	results := []any{}
	for i := 0; i < 40; i++ {
		results = append(results, agentcontract.JSON{"status": "partial", "audit": agentcontract.JSON{"trace": strings.Repeat("x", 12000)}})
	}
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "monitor.batch", SourceVersion: "1", Value: agentcontract.JSON{"data": agentcontract.JSON{"results": results}}, Quality: "provider_reported", ObservedAt: time.Now().UTC(), ReferenceScope: "durable"}
	model := &promptCaptureModel{decisions: []agentcontract.Decision{{Schema: "agenstra.decision.v1", Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "results"}}, {Schema: "agenstra.decision.v1", Kind: "inspect_fact", FactID: fact.FactID, Path: []any{"data", "results", 39, "status"}}, {Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Inspected", FactIDs: []string{fact.FactID}}}}
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
	decisions []agentcontract.Decision
	prompts   []string
}

func (m *promptCaptureModel) Decide(_ context.Context, _ agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
	m.prompts = append(m.prompts, prompt)
	return m.decisions[len(m.prompts)-1], nil
}

func TestRuntimeProtocolPromptDoesNotChangeProviderPrompt(t *testing.T) {
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
	original := provider.SystemPrompt()
	runtime := &AgentRuntime{Provider: provider}
	prompt := runtime.SystemPrompt()
	for _, field := range []string{"input_schema", "result_refs", "model_output"} {
		if !strings.Contains(prompt, field) {
			t.Fatalf("runtime prompt omits %s", field)
		}
	}
	if strings.Contains(prompt, "search_capabilities") {
		t.Fatal("disabled capability search advertised")
	}
	runtime.MaxContextCapabilities = 5
	if !strings.Contains(runtime.SystemPrompt(), "search_capabilities") {
		t.Fatal("enabled capability search not advertised")
	}
	if provider.SystemPrompt() != original {
		t.Fatal("runtime protocol changed provider prompt")
	}
}

func TestCoreFiniteJSONAndContextBudget(t *testing.T) {
	if _, err := agentcontract.CanonicalJSON(agentcontract.JSON{"bad": []any{math.NaN()}}); err == nil {
		t.Fatal("NaN accepted")
	}
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}

	// Keep the packet allowance constant while accounting for runtime guidance.
	runtime := &AgentRuntime{Provider: provider}
	budget := 3000 + utf8.RuneCountInString(runtime.SystemPrompt())
	runtime.MaxContextCharacters = budget
	state, err := runtime.NewState(strings.Repeat("x", 2150), "")
	if err != nil {
		t.Fatal(err)
	}
	items := []any{}
	for i := 0; i < 100; i++ {
		items = append(items, i)
	}
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"results": items}}, ReferenceScope: "durable"}
	state.Facts = []agentcontract.Fact{fact}
	packet := runtime.Context(state)
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)+len(runtime.SystemPrompt()) > budget {
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
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}
	model := &promptCaptureModel{decisions: []agentcontract.Decision{{Kind: "final", AnswerMarkdown: "完成"}}}
	runtime := &AgentRuntime{Provider: provider, Model: model}
	runtime.MaxContextCharacters = 1600 + utf8.RuneCountInString(runtime.SystemPrompt())
	state, err := runtime.NewState(strings.Repeat("航", 1000), "")
	if err != nil {
		t.Fatal(err)
	}
	packet := runtime.Context(state)
	raw, err := agentcontract.CanonicalJSON(packet)
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
	calls := []agentcontract.ToolCall{}
	for i := 0; i < 5; i++ {
		calls = append(calls, agentcontract.ToolCall{CallRef: fmt.Sprintf("call-%d", i), Capability: "x.read", Arguments: agentcontract.JSON{}, Reason: "read"})
	}
	model := &promptCaptureModel{decisions: []agentcontract.Decision{{Kind: "tool_batch", Calls: calls}, {Kind: "final", AnswerMarkdown: "corrected"}}}
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model}
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
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{"read.first": {Name: "read.first", Version: "1", Effect: "read"}, "write.second": {Name: "write.second", Version: "1", Effect: "write", ApprovalRequired: true}}}
	model := &promptCaptureModel{decisions: []agentcontract.Decision{{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "first", Capability: "read.first", Arguments: agentcontract.JSON{}, Reason: "read"}, {CallRef: "second", Capability: "write.second", Arguments: agentcontract.JSON{}, Reason: "write"}}}}}
	runtime := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"write.second": true}}
	result, err := runtime.Run(context.Background(), "read and write")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "needs_approval" || provider.called != 0 {
		t.Fatalf("approval bypass: status=%s calls=%d", result.Status, provider.called)
	}
}

func TestCoreFactPreviewNestedListKeepsSmallFields(t *testing.T) {
	rows := make([]any, 20)
	for i := range rows {
		rows[i] = agentcontract.JSON{
			"id": i + 10, "user_id": 2, "group_id": 10, "status": "active",
			"daily_usage_usd": 0, "weekly_usage_usd": 0, "monthly_usage_usd": 0,
			"daily_window_start": nil, "weekly_window_start": nil, "monthly_window_start": nil,
			"created_at": "2026-10-09T08:30:00.000000+08:00", "updated_at": "2026-10-09T08:30:00.000000+08:00",
			"starts_at": "2026-10-09T08:30:00.000000+08:00", "expires_at": "2026-11-09T08:30:00.000000+08:00",
			"revoked_at": "2026-10-09T08:30:00.000000+08:00",
		}
	}
	value := agentcontract.JSON{"status": "succeeded", "command_id": "command-1", "data": agentcontract.JSON{
		"command_id": "command-1", "result": agentcontract.JSON{"data": agentcontract.JSON{
			"total": 20, "page": 1, "page_size": 20, "pages": 1, "items": rows,
		}},
	}}
	before, err := agentcontract.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{3000, 6000} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			view := factView(agentcontract.Fact{Value: value}, budget)
			encoded, err := agentcontract.CanonicalJSON(view.Value)
			if err != nil {
				t.Fatal(err)
			}
			if utf8.RuneCount(encoded) > budget {
				t.Fatalf("preview exceeds budget: %d > %d", utf8.RuneCount(encoded), budget)
			}
			preview, err := agentcontract.ValueAt(view.Value, []any{"data", "result", "data", "items"})
			if err != nil {
				t.Fatal(err)
			}
			items, ok := preview.([]any)
			if !ok || len(items) != len(rows) {
				t.Fatalf("array indices changed: %v", preview)
			}
			for i, item := range items {
				row, ok := item.(agentcontract.JSON)
				if !ok {
					t.Fatalf("row %d is not an object", i)
				}
				for _, key := range []string{"id", "user_id", "group_id", "status"} {
					if got, exists := row[key]; !exists || got != rows[i].(agentcontract.JSON)[key] {
						t.Errorf("row %d lost %s", i, key)
					}
				}
			}
			if len(view.OmittedPaths) == 0 {
				t.Fatal("truncated values have no omission metadata")
			}
			after, err := agentcontract.CanonicalJSON(value)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("preview changed the retained Fact")
			}
			second, err := agentcontract.CanonicalJSON(factView(agentcontract.Fact{Value: value}, budget))
			if err != nil {
				t.Fatal(err)
			}
			first, err := agentcontract.CanonicalJSON(view)
			if err != nil || string(first) != string(second) {
				t.Fatal("preview is not deterministic", err)
			}
		})
	}
}
