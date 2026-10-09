package react

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type contextBudgetProvider struct {
	coreTestProvider
	skills map[string]agentcontract.Skill
}

func TestSelectedUnionSchemaDefersWithinContextBudget(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.write", InputSchema: agentcontract.JSON{"anyOf": []any{
		agentcontract.JSON{"properties": agentcontract.JSON{"operation": agentcontract.JSON{"const": "archive"}, "arguments": agentcontract.JSON{"type": "object", "description": strings.Repeat("x", 6000)}}},
	}}}
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, Grants: map[string]bool{cap.Name: true}, MaxContextCapabilities: 1}
	runtime.MaxContextCharacters = 2600 + utf8.RuneCountInString(runtime.SystemPrompt())
	state, err := runtime.NewState("Check the record contract", "")
	if err != nil {
		t.Fatal(err)
	}
	packet := runtime.Context(state)
	assertContextBudget(t, packet, runtime.SystemPrompt(), runtime.MaxContextCharacters)
	if len(packet.Capabilities) != 1 {
		t.Fatal("budget lost capability identity", packet.Capabilities)
	}
	view := packet.Capabilities[0]
	if view["schema_requires_inspection"] != true || !reflect.DeepEqual(view["input_fields"], []string{"arguments", "operation"}) {
		t.Fatal("budget lost union wrapper fields", view)
	}
}

func (p *contextBudgetProvider) Skills() map[string]agentcontract.Skill { return p.skills }

type contextBudgetModel func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error)

func (m contextBudgetModel) Decide(ctx context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
	return m(ctx, packet, prompt)
}

func contextBudgetFact(value agentcontract.JSON) agentcontract.Fact {
	return agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "record.read", SourceVersion: "1", Value: value, Quality: "provider_reported", ObservedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), ReferenceScope: "durable"}
}

func contextBudgetRuntime(t *testing.T, budget int) (*AgentRuntime, *agentcontract.RuntimeState, *contextBudgetProvider) {
	t.Helper()
	provider := &contextBudgetProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, skills: map[string]agentcontract.Skill{}}

	// Preserve the packet allowance while counting the runtime's fixed guidance.
	runtime := &AgentRuntime{Provider: provider, Grants: map[string]bool{}}
	runtime.MaxContextCharacters = budget + utf8.RuneCountInString(runtime.SystemPrompt())
	state, err := runtime.NewState("Read records", "")
	if err != nil {
		t.Fatal(err)
	}
	return runtime, state, provider
}

func assertContextBudget(t *testing.T, packet agentcontract.ContextPacket, prompt string, budget int) int {
	t.Helper()
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	size := utf8.RuneCount(raw) + utf8.RuneCountInString(prompt)
	if size > budget {
		t.Fatalf("model input exceeded budget: %d > %d", size, budget)
	}
	return size
}

func TestContextBudgetRestoresRecordedCallArgumentsAfterSchemaDeferral(t *testing.T) {
	runtime, state, provider := contextBudgetRuntime(t, 6500)
	runtime.MaxContextCapabilities = 1
	cap := agentcontract.CapabilityDescription{Name: "record.read", InputSchema: agentcontract.JSON{
		"type": "object", "properties": agentcontract.JSON{
			"method": agentcontract.JSON{"type": "string", "enum": []string{"summary", "snapshot", "trend"}},
			"query":  agentcontract.JSON{"type": "object", "description": strings.Repeat("x", 12000)},
		},
	}}
	provider.caps[cap.Name] = cap
	runtime.Grants[cap.Name] = true
	for i, method := range []string{"summary", "snapshot"} {
		fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"body": strings.Repeat("y", 1800)}})
		state.Facts = append(state.Facts, fact)
		state.ModelObservations = append(state.ModelObservations, agentcontract.Observation{
			CallRef: fmt.Sprintf("read-%d", i), Capability: cap.Name, Status: "succeeded", FactID: &fact.FactID,
			Arguments: agentcontract.JSON{"method": method, "query": agentcontract.JSON{"start": "2026-10-06", "end": "2026-10-07"}},
		})
	}
	latest := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"revision": 3}})
	state.Facts = append(state.Facts, latest)
	state.ModelObservations = append(state.ModelObservations, agentcontract.Observation{
		CallRef: "context-1", Capability: "context.read", Status: "succeeded", FactID: &latest.FactID, Arguments: agentcontract.JSON{},
	})
	before, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	packet := runtime.Context(state)
	assertContextBudget(t, packet, runtime.SystemPrompt(), runtime.MaxContextCharacters)
	if packet.Capabilities[0]["schema_requires_inspection"] != true {
		t.Fatal("large catalog schema did not exercise budget projection")
	}
	for i := range 2 {
		if packet.Observations[i].ArgumentsOmitted || !reflect.DeepEqual(packet.Observations[i].Arguments, state.ModelObservations[i].Arguments) {
			t.Fatalf("completed calls in the same capability lost their method and window: %+v", packet.Observations[i])
		}
	}
	value, err := agentcontract.ValueAt(packet.Facts[len(packet.Facts)-1].Value, []any{"data", "revision"})
	if err != nil || value != 3 {
		t.Fatal("restoring arguments displaced the latest result", value, err)
	}
	after, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("projection changed durable run state")
	}
}

func TestContextBudgetDoesNotRecoverInitiallyOmittedArguments(t *testing.T) {
	packet := agentcontract.ContextPacket{
		Schema: "agenstra.context.v1", Instruction: "Read the remaining records",
		Capabilities: []agentcontract.JSON{{"name": "record.read", "input_schema": agentcontract.JSON{
			"type": "object", "properties": agentcontract.JSON{"query": agentcontract.JSON{"type": "string", "description": strings.Repeat("x", 12000)}},
		}}},
		Observations: []agentcontract.Observation{
			{CallRef: "hidden-1", Capability: "record.read", Status: "succeeded", Arguments: agentcontract.JSON{}, ArgumentsOmitted: true},
			{CallRef: "read-2", Capability: "record.read", Status: "succeeded", Arguments: agentcontract.JSON{"query": "visible"}},
		},
	}
	state := &agentcontract.RuntimeState{ModelObservations: []agentcontract.Observation{
		{CallRef: "hidden-1", Capability: "record.read", Status: "succeeded", Arguments: agentcontract.JSON{"query": "not-in-model-projection"}},
	}}
	before, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	projected := budgetContext(packet, state, 2000)
	if contextCharacters(projected) > 2000 {
		t.Fatal("restoring arguments exceeded the input budget")
	}
	if !projected.Observations[0].ArgumentsOmitted || len(projected.Observations[0].Arguments) != 0 {
		t.Fatal("projection recovered initially omitted arguments", projected.Observations[0])
	}
	if projected.Observations[1].ArgumentsOmitted || projected.Observations[1].Arguments["query"] != "visible" {
		t.Fatal("schema deferral did not restore the fitting visible input", projected.Observations[1])
	}
	after, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("budget projection mutated the caller's packet")
	}
}

func TestContextBudgetHistoryKeepsRecentEvidenceAndCompleteState(t *testing.T) {
	runtime, state, _ := contextBudgetRuntime(t, 9000)
	for i := 0; i < 12; i++ {
		fact := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"id": fmt.Sprint(i), "body": strings.Repeat("x", 1500)}})
		state.Facts = append(state.Facts, fact)
		state.ModelObservations = append(state.ModelObservations, agentcontract.Observation{CallRef: fmt.Sprintf("read-%d", i), Capability: "record.read", Status: "succeeded", FactID: &fact.FactID, Arguments: agentcontract.JSON{"query": strings.Repeat("y", 1700)}})
	}
	original, callErr := agentcontract.CanonicalJSON(state)
	if callErr != nil {
		t.Error(callErr)
	}
	packet := runtime.Context(state)
	size := assertContextBudget(t, packet, runtime.SystemPrompt(), runtime.MaxContextCharacters)
	if len(packet.Facts) != 12 || len(packet.Observations) != 12 {
		t.Fatal("lost identities or recent outcomes", packet)
	}
	body, err := agentcontract.ValueAt(packet.Facts[11].Value, []any{"data", "body"})
	if err != nil || body != strings.Repeat("x", 1500) {
		t.Fatal("latest evidence was lost", body, err)
	}
	for i, fact := range packet.Facts {
		if fact.FactID != state.Facts[i].FactID {
			t.Fatal("fact identity changed")
		}
	}
	if packet.Observations[11].CallRef != "read-11" {
		t.Fatal("latest outcome was lost")
	}
	after, callErr2 := agentcontract.CanonicalJSON(state)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("projection changed complete run state")
	}
	var restored agentcontract.RuntimeState
	decoder := json.NewDecoder(bytes.NewReader(original))
	decoder.UseNumber()
	if err := decoder.Decode(&restored); err != nil {
		t.Fatal(err)
	}
	restoredPacket, err := agentcontract.CanonicalJSON(runtime.Context(&restored))
	if err != nil {
		t.Fatal(err)
	}
	originalPacket, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredPacket, originalPacket) {
		t.Fatal("checkpoint restore changed the projection")
	}
	first := state.Facts[0]
	value, err := ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": first.FactID, "path": []any{"data", "body"}}}, map[string]agentcontract.Fact{first.FactID: first}, runtime.ConnectionID, true)
	if err != nil || value != strings.Repeat("x", 1500) {
		t.Fatal("reference lost complete original value", value, err)
	}
	t.Logf("input characters=%d; fact identities=%d; outcomes=%d; latest body characters=1500", size, len(packet.Facts), len(packet.Observations))
}

func TestContextBudgetSkillsInspectionAndDeterminism(t *testing.T) {
	runtime, state, provider := contextBudgetRuntime(t, 6500)
	for _, name := range []string{"old", "new"} {
		provider.skills[name] = agentcontract.Skill{Description: agentcontract.SkillDescription{Name: name, Description: name}, Content: name + strings.Repeat("x", 2500)}
	}
	provider.skills["tiny"] = agentcontract.Skill{Description: agentcontract.SkillDescription{Name: "tiny", Description: "Tiny"}, Content: "Use IDs"}
	state.LoadedSkills = []string{"old", "tiny", "new"}
	state.Followups = []string{"region: east"}
	for i := 0; i < 4; i++ {
		state.Facts = append(state.Facts, contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"id": fmt.Sprint(i), "body": strings.Repeat("x", 1000)}}))
	}
	state.InspectedFact = agentcontract.JSON{"fact_id": state.Facts[0].FactID, "path": []any{"data", "id"}, "preview": agentcontract.JSON{"value": "0"}, "omitted_paths": []any{}}
	packet := runtime.Context(state)
	assertContextBudget(t, packet, runtime.SystemPrompt(), runtime.MaxContextCharacters)
	if !reflect.DeepEqual(packet.InspectedFact, state.InspectedFact) || !reflect.DeepEqual(packet.Followups, state.Followups) {
		t.Fatal("lost current inspection or followups")
	}
	if len(packet.LoadedSkills) != 2 || packet.LoadedSkills["tiny"] != provider.skills["tiny"].Content || packet.LoadedSkills["new"] != provider.skills["new"].Content || !slices.Contains(packet.ContextOmissions, "skill: old; read_skill to load again") {
		t.Fatal("latest skill not retained", packet.LoadedSkills, packet.ContextOmissions)
	}
	for i := 0; i < 20; i++ {
		if !reflect.DeepEqual(runtime.Context(state), packet) {
			t.Fatal("map iteration changed projection ordering")
		}
	}
	state.LoadedSkills = []string{"new", "tiny", "old"}
	reloaded := runtime.Context(state)
	if len(reloaded.LoadedSkills) != 2 || reloaded.LoadedSkills["tiny"] != provider.skills["tiny"].Content || reloaded.LoadedSkills["old"] != provider.skills["old"].Content || !slices.Contains(reloaded.ContextOmissions, "skill: new; read_skill to load again") {
		t.Fatal("evicted skill could not be reloaded", reloaded)
	}
}

func TestContextBudgetCatalogDefersSchemasWithoutLosingInspectedContract(t *testing.T) {
	runtime, state, provider := contextBudgetRuntime(t, 8000)
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("record.read-%d", i)
		runtime.Grants[name] = true
		provider.caps[name] = agentcontract.CapabilityDescription{Name: name, Version: "1", Description: "Read", Effect: "compute", InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string", "description": strings.Repeat("x", 1400)}}, "required": []string{"id"}}}
	}
	state.InspectedCapability = agentcontract.Strptr("record.read-7")
	packet := runtime.Context(state)
	assertContextBudget(t, packet, runtime.SystemPrompt(), runtime.MaxContextCharacters)
	deferred := 0
	for i, item := range packet.Capabilities {
		if item["name"] != fmt.Sprintf("record.read-%d", i) || item["authorized"] != true {
			t.Fatal("catalog identity or authorization changed", item)
		}
		if item["schema_requires_inspection"] == true {
			deferred++
			if _, exists := item["input_schema"]; exists || !reflect.DeepEqual(item["input_fields"], []string{"id"}) {
				t.Fatal("deferred schema not explicit", item)
			}
		}
	}
	got, callErr3 := agentcontract.CanonicalJSON(packet.InspectedCapability["input_schema"])
	if callErr3 != nil {
		t.Error(callErr3)
	}
	want, callErr4 := agentcontract.CanonicalJSON(provider.caps["record.read-7"].InputSchema)
	if callErr4 != nil {
		t.Error(callErr4)
	}
	if deferred == 0 || !bytes.Equal(got, want) {
		t.Fatal("inspection lost complete input schema", packet)
	}
}

func TestContextBudgetHistoryOmissionCount(t *testing.T) {
	runtime, state, _ := contextBudgetRuntime(t, 1200)
	for i := 0; i < 20; i++ {
		state.ModelObservations = append(state.ModelObservations, agentcontract.Observation{CallRef: fmt.Sprintf("read-%d", i), Capability: "record.read", Status: "failed", ErrorCode: agentcontract.Strptr("upstream_unavailable"), Arguments: agentcontract.JSON{}})
	}
	packet := runtime.Context(state)
	assertContextBudget(t, packet, runtime.SystemPrompt(), runtime.MaxContextCharacters)
	if len(packet.Observations) >= 12 || packet.Observations[len(packet.Observations)-1].CallRef != "read-19" || !reflect.DeepEqual(packet.ContextOmissions, []string{fmt.Sprintf("observations: %d older entries", 20-len(packet.Observations))}) {
		t.Fatal("history or omission count incorrect", packet)
	}
}

func TestContextBudgetReferenceAvailability(t *testing.T) {
	for _, scope := range []string{"expired", "other_connection", "current_connection"} {
		t.Run(scope, func(t *testing.T) {
			runtime, state, _ := contextBudgetRuntime(t, 8000)
			for i := 0; i < 12; i++ {
				state.Facts = append(state.Facts, contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"id": fmt.Sprint(i), "body": strings.Repeat("x", 1000)}}))
			}
			for _, i := range []int{0, 11} {
				fact := &state.Facts[i]
				if scope == "expired" {
					expired := time.Now().Add(-time.Second)
					fact.ExpiresAt = &expired
				} else {
					fact.ReferenceScope = "connection"
					fact.ConnectionID = agentcontract.Strptr("old")
					if scope == "current_connection" {
						fact.ConnectionID = &runtime.ConnectionID
					}
				}
			}
			packet := runtime.Context(state)
			assertContextBudget(t, packet, runtime.SystemPrompt(), runtime.MaxContextCharacters)
			available := scope == "current_connection"
			if packet.Facts[0].ReferenceAvailable != available || packet.Facts[11].ReferenceAvailable != available {
				t.Fatal("compaction changed reference availability")
			}
			if !available {
				_, err := ResolveArgument(agentcontract.JSON{"$fact_id": state.Facts[0].FactID}, map[string]agentcontract.Fact{state.Facts[0].FactID: state.Facts[0]}, runtime.ConnectionID, true)
				if err == nil || err.Error() != "fact_reference_expired" {
					t.Fatal("compaction reactivated an unavailable reference", err)
				}
			}
		})
	}
}

func TestContextBudgetRequiredInformationFailsBeforeModelIO(t *testing.T) {
	for _, field := range []string{"instruction", "followups", "inspected_fact", "inspected_capability", "skill"} {
		t.Run(field, func(t *testing.T) {
			runtime, state, provider := contextBudgetRuntime(t, 2000)
			model := &hostModel{}
			runtime.Model = model
			switch field {
			case "instruction":
				state.Instruction = strings.Repeat("x", 5000)
			case "followups":
				state.Followups = []string{strings.Repeat("x", 5000)}
			case "inspected_fact":
				state.InspectedFact = agentcontract.JSON{"preview": agentcontract.JSON{"value": strings.Repeat("x", 5000)}}
			case "inspected_capability":
				runtime.Grants["record.read"] = true
				provider.caps["record.read"] = agentcontract.CapabilityDescription{Name: "record.read", InputSchema: agentcontract.JSON{"description": strings.Repeat("x", 5000)}}
				state.InspectedCapability = agentcontract.Strptr("record.read")
			case "skill":
				provider.skills["current"] = agentcontract.Skill{Description: agentcontract.SkillDescription{Name: "current", Description: "Current"}, Content: strings.Repeat("x", 5000)}
				state.LoadedSkills = []string{"current"}
			}
			before, callErr5 := agentcontract.CanonicalJSON(runtime.Context(state))
			if callErr5 != nil {
				t.Error(callErr5)
			}
			if err := runtime.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			after, callErr6 := agentcontract.CanonicalJSON(runtime.Context(state))
			if callErr6 != nil {
				t.Error(callErr6)
			}
			if state.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "context_too_large" || state.RoundsUsed != 0 || model.calls != 0 || !bytes.Equal(before, after) {
				t.Fatal("required information discarded or model called", state, model.calls)
			}
		})
	}
}

func TestContextBudgetRepairFeedbackSharesTheInputBudget(t *testing.T) {
	runtime, state, _ := contextBudgetRuntime(t, 1200)
	for i := 0; i < 20; i++ {
		state.ModelObservations = append(state.ModelObservations, agentcontract.Observation{CallRef: fmt.Sprintf("read-%d", i), Capability: "record.read", Status: "failed", Arguments: agentcontract.JSON{}})
	}
	calls := 0
	runtime.Model = contextBudgetModel(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		assertContextBudget(t, packet, prompt, runtime.MaxContextCharacters)
		calls++
		if calls == 1 {
			return agentcontract.Decision{}, agentcontract.ModelDecisionError{Kind: "model_decision_invalid"}
		}
		if !strings.Contains(prompt, "previous response") {
			t.Fatal("repair feedback lost")
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Done"}, nil
	})
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "completed" || calls != 2 || state.RoundsUsed != 2 {
		t.Fatal("repair failed under pressure", state, calls)
	}
}

func TestContextBudgetMultiRoundInspectAndForwardCompleteValue(t *testing.T) {
	provider := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{}}
	for _, name := range []string{"record.read", "record.forward"} {
		provider.caps[name] = agentcontract.CapabilityDescription{Name: name, Version: "1", Description: name, Effect: "read", InputSchema: agentcontract.JSON{}}
	}
	received := ""
	provider.hook = func(_ context.Context, name string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		if name == "record.read" {
			return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": fmt.Sprintf("R-%v", args["index"]), "body": strings.Repeat("x", 9000)}}, nil
		}
		received = args["payload"].(string)
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"received_characters": len(received)}}, nil
	}
	rounds := 0
	budget := 6500 + utf8.RuneCountInString(decisionProtocolPrompt)
	model := contextBudgetModel(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		assertContextBudget(t, packet, prompt, budget)
		rounds++
		switch rounds {
		case 1:
			calls := []agentcontract.ToolCall{}
			for i := 0; i < 4; i++ {
				calls = append(calls, agentcontract.ToolCall{CallRef: fmt.Sprintf("read-%d", i), Capability: "record.read", Arguments: agentcontract.JSON{"index": i, "query": strings.Repeat("y", 1700)}, Reason: "Read records"})
			}
			return agentcontract.Decision{Kind: "tool_batch", Calls: calls}, nil
		case 2:
			if len(packet.Facts) != 4 || len(packet.Facts[0].OmittedPaths) == 0 {
				t.Fatal("large results were not projected", packet)
			}
			return agentcontract.Decision{Kind: "inspect_fact", FactID: packet.Facts[0].FactID, Path: []any{"data", "id"}}, nil
		case 3:
			if packet.InspectedFact["preview"].(map[string]any)["value"] != "R-0" {
				t.Fatal("could not inspect old evidence", packet.InspectedFact)
			}
			return agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "forward", Capability: "record.forward", Arguments: agentcontract.JSON{"payload": agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": packet.Facts[0].FactID, "path": []any{"data", "body"}}}}, Reason: "Forward complete stored body"}}}, nil
		case 4:
			value, err := agentcontract.ValueAt(packet.Facts[4].Value, []any{"data", "received_characters"})
			if err != nil || value != 9000 {
				t.Fatal("latest result missing", value, err)
			}
			if _, ok := packet.Observations[len(packet.Observations)-1].Arguments["payload"].(map[string]any); !ok {
				t.Fatal("model did not receive original reference arguments")
			}
			ids := []string{}
			for _, fact := range packet.Facts {
				ids = append(ids, fact.FactID)
			}
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Forwarded 9,000 characters", FactIDs: ids}, nil
		default:
			t.Fatal("unexpected model round", rounds)
			return agentcontract.Decision{}, nil
		}
	})
	runtime := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"record.read": true, "record.forward": true}, MaxContextCharacters: budget}
	result, err := runtime.Run(t.Context(), "Read records and forward the complete body of the first record")
	if err != nil || result.Status != "completed" || rounds != 4 || len(result.Facts) != 5 || received != strings.Repeat("x", 9000) || result.Observations[4].Arguments["payload"] != received {
		t.Fatal("multi-round run lost complete evidence", result, err)
	}
}
