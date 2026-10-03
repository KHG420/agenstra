package agenstra

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContextTelemetryMeasuresFinalProjection(t *testing.T) {
	r, state, _ := contextBudgetRuntime(t, 9000)
	state.Instruction = "查看中文资料"
	for i := 0; i < 12; i++ {
		state.Facts = append(state.Facts, contextBudgetFact(JSON{"body": strings.Repeat("汉", 1500)}))
	}
	r.Model = contextBudgetModel(func(_ctx context.Context, packet ContextPacket, prompt string) (Decision, error) {
		raw, callErr := CanonicalJSON(packet)
		if callErr != nil {
			t.Error(callErr)
		}
		if state.ContextTelemetry == nil || state.ContextTelemetry.InputCharacters != utf8.RuneCount(raw)+utf8.RuneCountInString(prompt) {
			t.Fatal("not the final request")
		}
		return Decision{Kind: "request_input", Field: "confirm", Prompt: "确认"}, nil
	})
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	c := state.ContextTelemetry
	sum := 0
	for _, n := range c.Components {
		sum += n
	}
	if sum != c.InputCharacters || c.CandidateCharacters <= c.InputCharacters || c.Omissions.FactPaths == 0 || c.OverLimit {
		t.Fatalf("%+v", c)
	}
	if state.Facts[0].Value["body"] != strings.Repeat("汉", 1500) {
		t.Fatal("stored Fact changed")
	}
}

func TestContextTelemetryRecordsOversizedRequiredInput(t *testing.T) {
	r, state, _ := contextBudgetRuntime(t, 1000)
	state.Instruction = strings.Repeat("required", 1000)
	m := &coreTestModel{}
	r.Model = m
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if m.calls != 0 || state.ContextTelemetry == nil || !state.ContextTelemetry.OverLimit || state.ContextTelemetry.CharactersRemaining != 0 {
		t.Fatalf("%+v", state.ContextTelemetry)
	}
}
