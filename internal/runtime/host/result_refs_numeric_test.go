package host

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestHostNumericResultReferencesUsePersistedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value json.Number
		id    string
	}{
		{name: "decimal integer", value: "42.0", id: "42"},
		{name: "exponent integer", value: "4.2e1", id: "42"},
		{name: "exact large integer", value: "18446744073709551615", id: "18446744073709551615"},
		{name: "decimal large integer", value: "18446744073709551615.0", id: "18446744073709551615"},
		{name: "exponent large integer", value: "1.8446744073709551615e19", id: "18446744073709551615"},
		{name: "fraction corrected without repeating read", value: "1.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &hostProvider{hook: func(context.Context, string, map[string]any, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
				return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": tc.value}, ReferenceScope: "durable"}, nil
			}}
			h := testHost(t, testStore(t), provider, &hostModel{})
			rounds := 0
			h.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
				rounds++
				if len(packet.Facts) == 0 {
					return callDecision("records.get"), nil
				}
				factID := packet.Facts[0].FactID
				final := agentcontract.Decision{Kind: "final", AnswerMarkdown: "Record", FactIDs: []string{factID}}
				if rounds == 2 {
					final.ResultRefs = []agentcontract.ResultRefRequest{{FactID: factID, Path: []any{"data", "id"}}}
				} else {
					if tc.id != "" || len(packet.Observations) != 2 || packet.Observations[1].ErrorCode == nil || *packet.Observations[1].ErrorCode != "final_result_refs_invalid" {
						t.Fatal("missing numeric reference correction feedback", packet.Observations)
					}
				}
				return final, nil
			})
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil {
				t.Fatal(err)
			}
			state, err := h.Restore(run)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != "completed" || provider.calls != 1 || len(state.Facts) != 1 {
				t.Fatal(run.Status, provider.calls, state.Facts)
			}
			data, ok := state.Facts[0].Value["data"].(map[string]any)
			if !ok {
				t.Fatal("missing persisted business data", state.Facts[0].Value)
			}
			value, ok := data["id"].(json.Number)
			if !ok {
				t.Fatal("persisted ID is no longer a JSON number", data["id"])
			}
			before, validBefore := new(big.Rat).SetString(string(tc.value))
			after, validAfter := new(big.Rat).SetString(string(value))
			if !validBefore || !validAfter || before.Cmp(after) != 0 {
				t.Fatalf("persisted evidence changed exact numeric value: %s -> %s", tc.value, value)
			}
			if tc.id == "" {
				if len(state.ResultRefs) != 0 || rounds != 3 {
					t.Fatal("fractional reference accepted or read repeated", state.ResultRefs, rounds)
				}
			} else if len(state.ResultRefs) != 1 || state.ResultRefs[0].ID != tc.id || rounds != 2 {
				t.Fatal("persisted numeric identity changed", state.ResultRefs, rounds)
			}
		})
	}
}
