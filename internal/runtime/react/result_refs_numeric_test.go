package react

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestFinalResultRefsValidateIntegerIDsAcrossNumberRepresentations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		id    string
	}{
		{name: "integer", value: 42, id: "42"},
		{name: "int64", value: int64(-42), id: "-42"},
		{name: "float integer", value: float64(42), id: "42"},
		{name: "float fraction", value: 1.5},
		{name: "float outside safe range", value: float64(9007199254740992)},
		{name: "json integer", value: json.Number("42"), id: "42"},
		{name: "json decimal integer", value: json.Number("42.0"), id: "42"},
		{name: "json exponent integer", value: json.Number("4.2e1"), id: "42"},
		{name: "json negative exponent integer", value: json.Number("4200e-2"), id: "42"},
		{name: "json exact large integer", value: json.Number("18446744073709551615"), id: "18446744073709551615"},
		{name: "json large decimal integer", value: json.Number("18446744073709551615.0"), id: "18446744073709551615"},
		{name: "json fraction", value: json.Number("1.5")},
		{name: "json tiny fraction", value: json.Number("1e-999999999")},
		{name: "json close fraction", value: json.Number("1.00000000000000000001")},
		{name: "json large fraction", value: json.Number("9007199254740991.1")},
		{name: "json negative zero", value: json.Number("-0.0"), id: "0"},
		{name: "json zero huge exponent", value: json.Number("0e999999999"), id: "0"},
		{name: "json oversized result", value: json.Number("1e256")},
		{name: "json maximum result", value: json.Number("1e255"), id: "1" + strings.Repeat("0", 255)},
		{name: "string ID unchanged", value: "42.0", id: "42.0"},
		{name: "business UUID unchanged", value: "bd6085a1-68e4-48dc-a7a4-09484c2b8333", id: "bd6085a1-68e4-48dc-a7a4-09484c2b8333"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: "records.get", ReferenceScope: "durable", Value: agentcontract.JSON{"data": agentcontract.JSON{"id": tc.value}}}
			r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
				return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Record", FactIDs: []string{fact.FactID}, ResultRefs: []agentcontract.ResultRefRequest{{FactID: fact.FactID, Path: []any{"data", "id"}}}}, nil
			})}
			state, err := r.NewState("Find record", "")
			if err != nil {
				t.Fatal(err)
			}
			state.Facts = []agentcontract.Fact{fact}
			if err = r.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			if tc.id == "" {
				if state.Status == "completed" || len(state.ResultRefs) != 0 || len(state.Observations) != 1 || state.Observations[0].ErrorCode == nil || *state.Observations[0].ErrorCode != "final_result_refs_invalid" {
					t.Fatalf("invalid numeric ID was published: status=%s refs=%v observations=%v", state.Status, state.ResultRefs, state.Observations)
				}
			} else if state.Status != "completed" || len(state.ResultRefs) != 1 || state.ResultRefs[0].ID != tc.id {
				t.Fatalf("exact integer ID changed: status=%s refs=%v want=%q", state.Status, state.ResultRefs, tc.id)
			}
		})
	}
}
