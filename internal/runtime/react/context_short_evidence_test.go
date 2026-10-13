package react

import (
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestContextBudgetKeepsCompleteShortEvidenceBeforeLargeOlderPreviews(t *testing.T) {
	for _, allowance := range []int{8000, 9000, 10000} {
		t.Run(fmt.Sprint(allowance), func(t *testing.T) {
			r, state, _ := contextBudgetRuntime(t, allowance)
			short := contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{
				"success": false, "count": 0, "missing": nil,
				"private": strings.Repeat("hidden-credential", 1000),
			}})
			short.ModelOutput = &agentcontract.ModelOutput{Paths: [][]string{{"success"}, {"count"}, {"missing"}}}
			state.Facts = append(state.Facts, short)
			for i := range 3 {
				rows := []any{}
				for j := range 64 {
					rows = append(rows, agentcontract.JSON{"id": j, "body": strings.Repeat(fmt.Sprint(i), 150)})
				}
				state.Facts = append(state.Facts, contextBudgetFact(agentcontract.JSON{"data": agentcontract.JSON{"rows": rows}}))
			}
			before, err := agentcontract.CanonicalJSON(state)
			if err != nil {
				t.Fatal(err)
			}
			packet := r.Context(state)
			assertContextBudget(t, packet, r.SystemPrompt(), r.MaxContextCharacters)
			if len(packet.Facts[0].OmittedPaths) != 0 {
				t.Fatal("short authorized evidence was displaced by partial large previews", packet.Facts[0].OmittedPaths)
			}
			want := agentcontract.JSON{"data": agentcontract.JSON{"success": false, "count": 0, "missing": nil}}
			valueJSON, valueErr := agentcontract.CanonicalJSON(packet.Facts[0].Value)
			wantJSON, wantErr := agentcontract.CanonicalJSON(want)
			if valueErr != nil || wantErr != nil || string(valueJSON) != string(wantJSON) {
				t.Fatal("short evidence changed or exposed private content", packet.Facts[0].Value)
			}
			for _, field := range []string{"success", "count", "missing"} {
				got, readErr := agentcontract.ValueAt(packet.Facts[0].Value, []any{"data", field})
				if readErr != nil || got != want["data"].(agentcontract.JSON)[field] {
					t.Fatal("false, zero or null evidence changed", field, got, readErr)
				}
			}
			latest := packet.Facts[len(packet.Facts)-1]
			if rows, readErr := agentcontract.ValueAt(latest.Value, []any{"data", "rows"}); readErr != nil || len(rows.([]any)) == 0 {
				t.Fatal("short evidence displaced the recent preview", latest, readErr)
			}
			after, err := agentcontract.CanonicalJSON(state)
			if err != nil || string(before) != string(after) {
				t.Fatal("budget projection modified complete state", err)
			}
		})
	}
}
