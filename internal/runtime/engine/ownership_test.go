package engine

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

type ownershipProvider struct {
	coreTestProvider
	invoke   func(JSON) CapabilityResult
	identity func(*InvocationContext)
}

func (p *ownershipProvider) Invoke(_ context.Context, _ string, args JSON, inv *InvocationContext) (CapabilityResult, error) {
	p.called++
	if p.identity != nil {
		p.identity(inv)
	}
	return p.invoke(args), nil
}

func TestExecuteCallOwnsArgumentsAndEvidence(t *testing.T) {
	cap := CapabilityDescription{Name: "records.get", Version: "1", Effect: "read", ModelOutput: &ModelOutput{Paths: [][]string{{"record", "id"}}}}
	data := JSON{"record": JSON{"id": "original", "count": 7}, "labels": []string{"original"}}
	expires := time.Now().Add(time.Hour)
	p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}, invoke: func(args JSON) CapabilityResult {
		args["filter"].(JSON)["id"] = "provider mutation"
		return CapabilityResult{Data: data, ExpiresAt: &expires}
	}, identity: func(inv *InvocationContext) {
		inv.ConnectionID, inv.TargetSubject = "provider mutation", "provider mutation"
	}}
	args := JSON{"filter": JSON{"id": "requested"}}
	inv := &InvocationContext{ConnectionID: "connection"}
	outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, ToolCall{Capability: cap.Name, Arguments: args}, inv)
	if err != nil || outcome.Fact == nil {
		t.Fatal(outcome, err)
	}
	if args["filter"].(JSON)["id"] != "requested" {
		t.Error("provider mutated saved arguments")
	}
	if inv.ConnectionID != "connection" || inv.TargetSubject != "" || *outcome.Fact.ConnectionID != "connection" || outcome.Fact.SourceSubject != "" {
		t.Error("provider changed the trusted invocation identity")
	}
	if outcome.Fact.Value["data"].(JSON)["record"].(JSON)["count"] != 7 {
		t.Error("copying evidence changed a Go integer")
	}
	labels, ok := outcome.Fact.Value["data"].(JSON)["labels"].([]string)
	if !ok || labels[0] != "original" {
		t.Fatal("copying evidence changed a typed slice")
	}
	data["record"].(JSON)["id"] = "later mutation"
	data["labels"].([]string)[0] = "later mutation"
	cap.ModelOutput.Paths[0][0] = "later mutation"
	expires = expires.Add(time.Hour)
	inv.ConnectionID = "later connection"
	if outcome.Fact.Value["data"].(JSON)["record"].(JSON)["id"] != "original" || labels[0] != "original" || outcome.Fact.ModelOutput.Paths[0][0] != "record" || *outcome.Fact.ConnectionID != "connection" || outcome.Fact.ExpiresAt.Equal(expires) {
		t.Error("provider or caller mutated retained evidence")
	}
}

func TestExecuteCallRejectsNonJSONBoundaries(t *testing.T) {
	cap := CapabilityDescription{Name: "records.get", Version: "1", Effect: "read"}
	p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}, invoke: func(JSON) CapabilityResult {
		return CapabilityResult{Data: JSON{"value": math.NaN()}}
	}}
	outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, ToolCall{Capability: cap.Name, Arguments: JSON{"value": math.NaN()}}, nil)
	if err != nil || outcome.ErrorCode != "capability_input_invalid" || p.called != 0 {
		t.Fatalf("invalid input dispatched: %+v, %v, calls=%d", outcome, err, p.called)
	}
	outcome, err = ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, ToolCall{Capability: cap.Name, Arguments: JSON{}}, nil)
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
	cap := CapabilityDescription{Name: "records.get", Version: "1", Effect: "read"}
	p := &ownershipProvider{coreTestProvider: coreTestProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}, invoke: func(JSON) CapabilityResult {
		return CapabilityResult{Data: JSON{"record": data, "typed_record": recordPointer(data)}}
	}}
	outcome, err := ExecuteCall(t.Context(), p, map[string]bool{cap.Name: true}, ToolCall{Capability: cap.Name, Arguments: JSON{}}, nil)
	if err != nil || outcome.Fact == nil {
		t.Fatal(outcome, err)
	}
	copy := outcome.Fact.Value["data"].(JSON)["record"].(*record)
	if copy == data || copy.Count != 7 || copy.cache != nil || !copy.Lock.TryLock() {
		t.Fatal("JSON evidence retained private state or copied an acquired lock")
	}
	copy.Lock.Unlock()
	typedCopy, ok := outcome.Fact.Value["data"].(JSON)["typed_record"].(recordPointer)
	if !ok || typedCopy == recordPointer(data) || typedCopy.Count != 7 {
		t.Fatal("copying JSON evidence changed a named Go pointer type")
	}
	data.Labels[0] = "later mutation"
	if copy.Labels[0] != "original" {
		t.Fatal("typed struct evidence retained a mutable slice")
	}
}

type ownershipModel struct{}

func (ownershipModel) MeasureInput(packet ContextPacket, _ string) (InputMeasurement, error) {
	packet.Facts[0].Value["data"].(JSON)["id"] = "measurement mutation"
	packet.Observations[0].Arguments["filter"].(JSON)["id"] = "measurement mutation"
	packet.Followups[0] = "measurement mutation"
	packet.InspectedFact["preview"].(JSON)["value"] = "measurement mutation"
	return InputMeasurement{Tokens: 100, Source: "test"}, nil
}

func (ownershipModel) Decide(_ context.Context, packet ContextPacket, _ string) (Decision, error) {
	packet.Facts[0].Value["data"].(JSON)["id"] = "model mutation"
	packet.Observations[0].Arguments["filter"].(JSON)["id"] = "model mutation"
	packet.Followups[0] = "model mutation"
	packet.InspectedFact["preview"].(JSON)["value"] = "model mutation"
	return Decision{Kind: "final", AnswerMarkdown: "done", FactIDs: []string{packet.Facts[0].FactID}}, nil
}

func TestModelContextCannotMutateRuntimeEvidence(t *testing.T) {
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: ownershipModel{}}
	state, err := runtime.NewState("read evidence", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []Fact{{FactID: NewID(), Value: JSON{"data": JSON{"id": "original"}}}}
	state.ModelObservations = []Observation{{Arguments: JSON{"filter": JSON{"id": "requested"}}}}
	state.Followups = []string{"original followup"}
	state.InspectedFact = JSON{"preview": JSON{"value": "original preview"}}
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Facts[0].Value["data"].(JSON)["id"] != "original" || state.ModelObservations[0].Arguments["filter"].(JSON)["id"] != "requested" || state.Followups[0] != "original followup" || state.InspectedFact["preview"].(JSON)["value"] != "original preview" {
		t.Error("model callback mutated runtime evidence")
	}
	packet := runtime.Context(state)
	if _, err := (ownershipModel{}).Decide(t.Context(), packet, ""); err != nil {
		t.Fatal(err)
	}
	if state.Facts[0].Value["data"].(JSON)["id"] != "original" || state.Followups[0] != "original followup" {
		t.Error("public context view retained mutable run evidence")
	}
}

func TestRuntimeRejectsInvalidEvidenceBeforeModelProjection(t *testing.T) {
	cycle := JSON{}
	cycle["self"] = cycle
	for name, value := range map[string]JSON{"nonfinite": {"value": math.NaN()}, "cycle": cycle} {
		t.Run(name, func(t *testing.T) {
			runtime := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{}}, Model: ownershipModel{}}
			state, err := runtime.NewState("read evidence", "")
			if err != nil {
				t.Fatal(err)
			}
			state.Facts = []Fact{{FactID: NewID(), Value: value}}
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
	packet := ContextPacket{InspectedFact: JSON{"value": math.NaN()}}
	_, _, fits := runtime.characterProjection(&RuntimeState{}, packet, "required instructions")
	if fits {
		t.Fatal("invalid context fit the character budget through integer overflow")
	}
}
