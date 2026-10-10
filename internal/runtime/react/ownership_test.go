package react

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type ownershipProvider struct {
	coreTestProvider
	invoke   func(agentcontract.JSON) agentcontract.CapabilityResult
	identity func(*agentcontract.InvocationContext)
}

func (p *ownershipProvider) Invoke(_ context.Context, _ string, args agentcontract.JSON, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
	p.called++
	if p.identity != nil {
		p.identity(inv)
	}
	return p.invoke(args), nil
}

func TestExecuteCallOwnsArgumentsAndEvidence(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", Effect: "read", ModelOutput: &agentcontract.ModelOutput{Paths: [][]string{{"record", "id"}}}}
	data := agentcontract.JSON{"record": agentcontract.JSON{"id": "original", "count": 7}, "labels": []string{"original"}}
	expires := time.Now().Add(time.Hour)
	p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, invoke: func(args agentcontract.JSON) agentcontract.CapabilityResult {
		args["filter"].(agentcontract.JSON)["id"] = "provider mutation"
		return agentcontract.CapabilityResult{Data: data, ExpiresAt: &expires}
	}, identity: func(inv *agentcontract.InvocationContext) {
		inv.ConnectionID, inv.TargetSubject = "provider mutation", "provider mutation"
	}}
	args := agentcontract.JSON{"filter": agentcontract.JSON{"id": "requested"}}
	inv := &agentcontract.InvocationContext{ConnectionID: "connection"}
	outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: args}, inv)
	if err != nil || outcome.Fact == nil {
		t.Fatal(outcome, err)
	}
	if args["filter"].(agentcontract.JSON)["id"] != "requested" {
		t.Error("provider mutated saved arguments")
	}
	if inv.ConnectionID != "connection" || inv.TargetSubject != "" || *outcome.Fact.ConnectionID != "connection" || outcome.Fact.SourceSubject != "" {
		t.Error("provider changed the trusted invocation identity")
	}
	if outcome.Fact.Value["data"].(agentcontract.JSON)["record"].(agentcontract.JSON)["count"] != 7 {
		t.Error("copying evidence changed a Go integer")
	}
	labels, ok := outcome.Fact.Value["data"].(agentcontract.JSON)["labels"].([]string)
	if !ok || labels[0] != "original" {
		t.Fatal("copying evidence changed a typed slice")
	}
	data["record"].(agentcontract.JSON)["id"] = "later mutation"
	data["labels"].([]string)[0] = "later mutation"
	cap.ModelOutput.Paths[0][0] = "later mutation"
	expires = expires.Add(time.Hour)
	inv.ConnectionID = "later connection"
	if outcome.Fact.Value["data"].(agentcontract.JSON)["record"].(agentcontract.JSON)["id"] != "original" || labels[0] != "original" || outcome.Fact.ModelOutput.Paths[0][0] != "record" || *outcome.Fact.ConnectionID != "connection" || outcome.Fact.ExpiresAt.Equal(expires) {
		t.Error("provider or caller mutated retained evidence")
	}
}

func TestExecuteCallValidatesCustomProviderInput(t *testing.T) {
	schema := agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
		"request": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"count": agentcontract.JSON{"type": "integer", "minimum": 0}, "key": agentcontract.JSON{"type": "string"}}, "required": []string{"count", "key"}, "additionalProperties": false},
	}, "required": []string{"request"}, "additionalProperties": false}
	for _, tc := range []struct {
		name string
		args agentcontract.JSON
	}{
		{"missing wrapper", agentcontract.JSON{"count": 0}},
		{"wrong type", agentcontract.JSON{"request": agentcontract.JSON{"count": "0"}}},
		{"extra property", agentcontract.JSON{"request": agentcontract.JSON{"count": 0, "extra": true}}},
		{"negative count", agentcontract.JSON{"request": agentcontract.JSON{"count": -1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := agentcontract.CapabilityDescription{Name: "records.write", InputSchema: schema, Effect: "write", IdempotencyArgument: []string{"request", "key"}}
			p := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
			outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: tc.args}, &agentcontract.InvocationContext{IdempotencyKey: "trusted-key"})
			if err != nil || outcome.ErrorCode != "capability_input_invalid" || outcome.Fact != nil || p.called != 0 {
				t.Fatalf("invalid input reached custom provider: %+v, error=%v, calls=%d", outcome, err, p.called)
			}
		})
	}
	cap := agentcontract.CapabilityDescription{Name: "records.write", InputSchema: schema, Effect: "write", IdempotencyArgument: []string{"request", "key"}}
	args := agentcontract.JSON{"request": agentcontract.JSON{"count": 0}}
	p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, invoke: func(input agentcontract.JSON) agentcontract.CapabilityResult {
		if input["request"].(agentcontract.JSON)["key"] != "trusted-key" {
			t.Fatal("validation ran before trusted idempotency binding", input)
		}
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"count": 0}}
	}}
	outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: args}, &agentcontract.InvocationContext{IdempotencyKey: "trusted-key"})
	if err != nil || outcome.Fact == nil || p.called != 1 || args["request"].(agentcontract.JSON)["key"] != nil {
		t.Fatal("valid input was rejected or caller arguments mutated", outcome, err, p.called, args)
	}
	cap.InputSchema = agentcontract.JSON{"type": "invalid-type"}
	p.caps[cap.Name] = cap
	outcome, err = ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: args}, &agentcontract.InvocationContext{IdempotencyKey: "trusted-key"})
	if err != nil || outcome.ErrorCode != "capability_input_invalid" || outcome.Fact != nil || p.called != 1 {
		t.Fatal("invalid declared schema dispatched", outcome, err, p.called)
	}
}

func TestExecuteCallRejectsNonJSONBoundaries(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", Effect: "read"}
	p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, invoke: func(agentcontract.JSON) agentcontract.CapabilityResult {
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"value": math.NaN()}}
	}}
	outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{"value": math.NaN()}}, nil)
	if err != nil || outcome.ErrorCode != "capability_input_invalid" || p.called != 0 {
		t.Fatalf("invalid input dispatched: %+v, %v, calls=%d", outcome, err, p.called)
	}
	outcome, err = ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{}}, nil)
	if err != nil || outcome.ErrorCode != "upstream_response_invalid" || outcome.Fact != nil {
		t.Fatalf("invalid evidence accepted: %+v, %v", outcome, err)
	}
}

func TestProviderEvidenceCopiesJSONFieldsWithoutCopyingLocks(t *testing.T) {
	type record struct {
		Lock   sync.Mutex `json:"-"`
		Count  int        `json:"count"`
		Labels []string   `json:"labels"`
		cache  map[string]string
	}
	type recordPointer *record
	data := &record{Count: 7, Labels: []string{"original"}, cache: map[string]string{"private": "owner only"}}
	data.Lock.Lock()
	defer data.Lock.Unlock()
	cap := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", Effect: "read"}
	p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, invoke: func(agentcontract.JSON) agentcontract.CapabilityResult {
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"record": data, "typed_record": recordPointer(data)}}
	}}
	outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, agentcontract.ToolCall{Capability: cap.Name, Arguments: agentcontract.JSON{}}, nil)
	if err != nil || outcome.Fact == nil {
		t.Fatal(outcome, err)
	}
	copy := outcome.Fact.Value["data"].(agentcontract.JSON)["record"].(*record)
	if copy == data || copy.Count != 7 || copy.cache != nil || !copy.Lock.TryLock() {
		t.Fatal("JSON evidence retained private state or copied an acquired lock")
	}
	copy.Lock.Unlock()
	typedCopy, ok := outcome.Fact.Value["data"].(agentcontract.JSON)["typed_record"].(recordPointer)
	if !ok || typedCopy == recordPointer(data) || typedCopy.Count != 7 {
		t.Fatal("copying JSON evidence changed a named Go pointer type")
	}
	data.Labels[0] = "later mutation"
	if copy.Labels[0] != "original" {
		t.Fatal("typed struct evidence retained a mutable slice")
	}
}

type ownershipModel struct{}

func (ownershipModel) MeasureInput(packet agentcontract.ContextPacket, _ string) (agentcontract.InputMeasurement, error) {
	packet.Facts[0].Value["data"].(agentcontract.JSON)["id"] = "measurement mutation"
	packet.Observations[0].Arguments["filter"].(agentcontract.JSON)["id"] = "measurement mutation"
	packet.Followups[0] = "measurement mutation"
	packet.InspectedFact["preview"].(agentcontract.JSON)["value"] = "measurement mutation"
	return agentcontract.InputMeasurement{Tokens: 100, Source: "test"}, nil
}

func (ownershipModel) Decide(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
	packet.Facts[0].Value["data"].(agentcontract.JSON)["id"] = "model mutation"
	packet.Observations[0].Arguments["filter"].(agentcontract.JSON)["id"] = "model mutation"
	packet.Followups[0] = "model mutation"
	packet.InspectedFact["preview"].(agentcontract.JSON)["value"] = "model mutation"
	return agentcontract.Decision{Kind: "final", AnswerMarkdown: "done", FactIDs: []string{packet.Facts[0].FactID}}, nil
}

func TestModelContextCannotMutateRuntimeEvidence(t *testing.T) {
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: ownershipModel{}}
	state, err := runtime.NewState("read evidence", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []agentcontract.Fact{{FactID: agentcontract.NewID(), Value: agentcontract.JSON{"data": agentcontract.JSON{"id": "original"}}}}
	state.ModelObservations = []agentcontract.Observation{{Arguments: agentcontract.JSON{"filter": agentcontract.JSON{"id": "requested"}}}}
	state.Followups = []string{"original followup"}
	state.InspectedFact = agentcontract.JSON{"preview": agentcontract.JSON{"value": "original preview"}}
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Facts[0].Value["data"].(agentcontract.JSON)["id"] != "original" || state.ModelObservations[0].Arguments["filter"].(agentcontract.JSON)["id"] != "requested" || state.Followups[0] != "original followup" || state.InspectedFact["preview"].(agentcontract.JSON)["value"] != "original preview" {
		t.Error("model callback mutated runtime evidence")
	}
	packet := runtime.Context(state)
	if _, err := (ownershipModel{}).Decide(t.Context(), packet, ""); err != nil {
		t.Fatal(err)
	}
	if state.Facts[0].Value["data"].(agentcontract.JSON)["id"] != "original" || state.Followups[0] != "original followup" {
		t.Error("public context view retained mutable run evidence")
	}
}

func TestRuntimeRejectsInvalidEvidenceBeforeModelProjection(t *testing.T) {
	cycle := agentcontract.JSON{}
	cycle["self"] = cycle
	for name, value := range map[string]agentcontract.JSON{"nonfinite": {"value": math.NaN()}, "cycle": cycle} {
		t.Run(name, func(t *testing.T) {
			runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: ownershipModel{}}
			state, err := runtime.NewState("read evidence", "")
			if err != nil {
				t.Fatal(err)
			}
			state.Facts = []agentcontract.Fact{{FactID: agentcontract.NewID(), Value: value}}
			if err := runtime.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			if state.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "run_state_invalid" || state.RoundsUsed != 0 {
				t.Fatalf("invalid evidence reached model projection: status=%s, error=%v, rounds=%d", state.Status, state.ErrorCode, state.RoundsUsed)
			}
		})
	}
}

func TestInvalidContextCannotFitCharacterBudget(t *testing.T) {
	runtime := &AgentRuntime{MaxContextCharacters: 1000}
	packet := agentcontract.ContextPacket{InspectedFact: agentcontract.JSON{"value": math.NaN()}}
	_, _, fits := runtime.characterProjection(&agentcontract.RuntimeState{}, packet, "required instructions")
	if fits {
		t.Fatal("invalid context fit the character budget through integer overflow")
	}
}
