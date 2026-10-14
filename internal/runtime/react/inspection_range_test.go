package react

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestStoredArrayRangeReadsPreserveHistoryAndPrivateEvidence(t *testing.T) {
	id := agentcontract.NewID()
	rows := []any{}
	for i := range 158 {
		rows = append(rows, agentcontract.JSON{"id": i, "amount": 0, "secret": "private-range-evidence"})
	}
	fact := agentcontract.Fact{FactID: id, Value: agentcontract.JSON{"data": agentcontract.JSON{"rows": rows}}, ReferenceScope: "durable", ModelOutput: &agentcontract.ModelOutput{Paths: [][]string{{"rows", "*", "id"}, {"rows", "*", "amount"}}}}
	decisions := []agentcontract.Decision{}
	for _, offset := range []int{0, 32, 64, 96, 128} {
		raw := fmt.Sprintf(`{"kind":"inspect_fact","fact_id":%q,"path":["data","rows"],"offset":%d,"limit":32}`, id, offset)
		decision, err := agentcontract.StrictDecision([]byte(raw))
		if err != nil {
			t.Fatalf("valid bounded range protocol rejected: %v", err)
		}
		decisions = append(decisions, decision)
	}
	runtime := &AgentRuntime{Provider: &coreTestProvider{}, Model: &coreTestModel{decisions: decisions}, MaxModelRounds: 10}
	state, err := runtime.NewState("Read the complete saved array in bounded ranges", "")
	if err != nil {
		t.Fatal(err)
	}
	state.Facts = []agentcontract.Fact{fact}
	for range 5 {
		if err := runtime.Step(context.Background(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored agentcontract.RuntimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	packet := runtime.Context(&restored)
	views := append(packet.InspectionHistory, packet.InspectedFact)
	if len(views) != 5 {
		t.Fatalf("range history deduplicated distinct pages: %d", len(views))
	}
	total := 0
	for i, view := range views {
		metadata, err := agentcontract.CanonicalJSON(view)
		if err != nil || strings.Contains(string(metadata), "private-range-evidence") {
			t.Fatalf("hidden field in range: %s %v", metadata, err)
		}
		lengthJSON, lengthErr := agentcontract.CanonicalJSON(view["array_length"])
		offsetJSON, offsetErr := agentcontract.CanonicalJSON(view["offset"])
		var length, offset float64
		lengthDecodeErr := json.Unmarshal(lengthJSON, &length)
		offsetDecodeErr := json.Unmarshal(offsetJSON, &offset)
		if lengthErr != nil || offsetErr != nil || lengthDecodeErr != nil || offsetDecodeErr != nil || length != 158 || offset != float64(i*32) {
			t.Fatalf("wrong authoritative range metadata: %+v", view)
		}
		preview := view["preview"].(map[string]any)["value"].([]any)
		if len(preview) != min(32, 158-i*32) {
			t.Fatalf("wrong returned page length: %d", len(preview))
		}
		omittedJSON, omittedErr := agentcontract.CanonicalJSON(view["omitted_paths"])
		if omittedErr != nil || string(omittedJSON) != "[]" {
			t.Fatal("small selected page unexpectedly incomplete")
		}
		total += len(preview)
	}
	if total != 158 {
		t.Fatalf("saved array incompletely read: %d", total)
	}
	if packet.InspectedFact["next_offset"] != nil {
		t.Fatal("last range incorrectly advertises a next page")
	}
	full, err := agentcontract.CanonicalJSON(restored.Facts[0].Value)
	if err != nil || !strings.Contains(string(full), "private-range-evidence") {
		t.Fatal("complete source evidence changed")
	}
	if len(restored.Observations) != 0 {
		t.Fatal("range inspection replayed business calls")
	}
}

func TestStoredArrayRangeProtocolRejectsInvalidAndNonArraySelection(t *testing.T) {
	id := agentcontract.NewID()
	for _, extra := range []string{`"offset":-1`, `"limit":0`, `"limit":33`, `"offset":1.5`, `"limit":"2"`} {
		raw := fmt.Sprintf(`{"kind":"inspect_fact","fact_id":%q,"path":["data"],%s}`, id, extra)
		if _, err := agentcontract.StrictDecision([]byte(raw)); err == nil {
			t.Fatalf("invalid range accepted: %s", extra)
		}
	}
	for _, path := range []string{`["data","object"]`, `["data","secret"]`} {
		raw := fmt.Sprintf(`{"kind":"inspect_fact","fact_id":%q,"path":%s,"offset":0,"limit":32}`, id, path)
		d, err := agentcontract.StrictDecision([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		runtime := &AgentRuntime{Provider: &coreTestProvider{}, Model: &coreTestModel{decisions: []agentcontract.Decision{d}}, MaxModelRounds: 3}
		state, err := runtime.NewState("Inspect stored evidence", "")
		if err != nil {
			t.Fatal(err)
		}
		state.Facts = []agentcontract.Fact{{FactID: id, ReferenceScope: "durable", Value: agentcontract.JSON{"data": agentcontract.JSON{"object": agentcontract.JSON{}, "secret": []any{"private"}}}, ModelOutput: &agentcontract.ModelOutput{Paths: [][]string{{"object"}}}}}
		if err := runtime.Step(context.Background(), state, nil); err != nil {
			t.Fatal(err)
		}
		if state.InspectedFact["error_code"] != "fact_reference_path_invalid" {
			t.Fatalf("non-array or private range accepted: %+v", state.InspectedFact)
		}
	}
}

func TestStoredArrayRangeCorrectionCarriesBoundsWithoutReplayingCalls(t *testing.T) {
	id := agentcontract.NewID()
	raw := fmt.Sprintf(`{"kind":"inspect_fact","fact_id":%q,"path":["data","rows"],"offset":32,"limit":126}`, id)
	invalid, rangeErr := agentcontract.StrictDecision([]byte(raw))
	if rangeErr == nil {
		t.Fatal("oversized page accepted")
	}
	calls := 0
	runtime := &AgentRuntime{Provider: &coreTestProvider{}, MaxModelRounds: 4}
	runtime.Model = decisionModelFunc(func(_ context.Context, _ agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		calls++
		if calls == 1 {
			return invalid, rangeErr
		}
		if !strings.Contains(prompt, "limit must be an integer in 1..32") || !strings.Contains(prompt, "next_offset") || !strings.Contains(prompt, "Never request all remaining array items") {
			t.Fatal("range bound feedback lost")
		}
		return agentcontract.StrictDecision([]byte(fmt.Sprintf(`{"kind":"inspect_fact","fact_id":%q,"path":["data","rows"],"offset":32,"limit":32}`, id)))
	})
	state, err := runtime.NewState("Inspect the saved next page", "")
	if err != nil {
		t.Fatal(err)
	}
	rows := []any{}
	for i := range 64 {
		rows = append(rows, i)
	}
	state.Facts = []agentcontract.Fact{{FactID: id, ReferenceScope: "durable", Value: agentcontract.JSON{"data": agentcontract.JSON{"rows": rows}}}}
	if err := runtime.Step(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || state.InspectedFact["offset"] != 32 || state.InspectedFact["returned_count"] != 32 || len(state.Observations) != 0 {
		t.Fatal("range repair did not retain bounded execution and evidence")
	}
}
