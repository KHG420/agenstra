package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNativeJSONFinalAnswerPreservesValuesWithoutStringRegeneration(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
	}{
		{"object", ` { "zero": 0, "false": false, "missing": null, "items": [] } `, `{"zero":0,"false":false,"missing":null,"items":[]}`},
		{"array", `[false, null, 0, ""]`, `[false,null,0,""]`},
		{"large decimal", `{"id":9007199254740993,"value":9007199254740993.1}`, `{"id":9007199254740993,"value":9007199254740993.1}`},
		{"escaped text", `{"text":"引号\"、括号}与换行\n","html":"<b>&</b>"}`, `{"text":"引号\"、括号}与换行\n","html":"<b>&</b>"}`},
		{"zero", `0`, `0`},
		{"false", `false`, `false`},
		{"null", `null`, `null`},
		{"string", `"quoted text"`, `"quoted text"`},
		{"empty object", `{}`, `{}`},
		{"empty array", `[]`, `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"schema":"agenstra.decision.v1","kind":"final","answer_json":` + tc.value + `,"fact_ids":["11111111-1111-4111-8111-111111111111"],"completion_feedback":""}`
			decision, err := StrictDecision([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if decision.Kind != "final" || decision.AnswerMarkdown != tc.want || !json.Valid([]byte(decision.AnswerMarkdown)) {
				t.Fatalf("native answer changed: kind=%s answer=%s; want %s", decision.Kind, decision.AnswerMarkdown, tc.want)
			}
			if len(decision.FactIDs) != 1 || decision.FactIDs[0] != "11111111-1111-4111-8111-111111111111" {
				t.Fatal("supporting evidence references changed")
			}
			if err := decision.Validate(); err != nil {
				t.Fatal("normalized decision does not obey the existing contract", err)
			}
		})
	}
}

func TestNativeJSONFinalAnswerRetainsExistingBoundsAndExclusiveFields(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"final","answer_json":{},"answer_markdown":"text","fact_ids":[]}`,
		`{"kind":"final","answer_json":{},"answer_markdown":"","fact_ids":[]}`,
		`{"kind":"final","answer_json":{},"answer_markdown":null,"fact_ids":[]}`,
		`{"kind":"final","answer_json":{},"fact_ids":[],"completion_feedback":null}`,
		`{"kind":"final","answer_json":{"value":1},"fact_ids":[],"extra":true}`,
		`{"kind":"final","answer_data":{},"fact_ids":[]}`,
		`{"kind":"final","fact_ids":[]}`,
		`{"kind":"approve_completion","answer_json":{}}`,
		`{"kind":"tool_batch","answer_json":{},"calls":[{"call_ref":"read","capability":"records.read","arguments":{},"reason":"read"}]}`,
		`{"kind":"final","answer_json":{"value":1}},"fact_ids":[]}`,
	} {
		if _, err := StrictDecision([]byte(raw)); err == nil {
			t.Fatalf("invalid or conflicting final answer accepted: %s", raw)
		}
	}
	for _, size := range []int{29998, 29999} {
		value, err := json.Marshal(strings.Repeat("中", size))
		if err != nil {
			t.Fatal(err)
		}
		raw := `{"kind":"final","answer_json":` + string(value) + `,"fact_ids":[]}`
		decision, err := StrictDecision([]byte(raw))
		if size == 29998 {
			if err != nil || utf8.RuneCountInString(decision.AnswerMarkdown) != 30000 {
				t.Fatal("existing rendered-answer Unicode boundary changed", err)
			}
		} else if err == nil {
			t.Fatal("oversized rendered native answer accepted")
		}
	}
}
