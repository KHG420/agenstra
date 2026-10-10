package agent

import (
	"strings"
	"testing"
)

func TestDecisionTextLimitsCountUnicodeCharacters(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		make  func(string) Decision
	}{
		{"reason", 500, func(text string) Decision {
			return Decision{Kind: "tool_batch", Calls: []ToolCall{{CallRef: "read-1", Capability: "records.read", Arguments: JSON{}, Reason: text}}}
		}},
		{"answer", 30000, func(text string) Decision {
			return Decision{Kind: "final", AnswerMarkdown: text}
		}},
		{"prompt", 1000, func(text string) Decision {
			return Decision{Kind: "request_input", Field: "label", Prompt: text}
		}},
		{"result label", 100, func(text string) Decision {
			return Decision{Kind: "final", AnswerMarkdown: "Ready", ResultRefs: []ResultRefRequest{{FactID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Path: []any{"data", "id"}, Label: text}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, character := range []string{"a", "中", "😀"} {
				for _, count := range []int{1, tc.limit, tc.limit + 1} {
					decision := tc.make(strings.Repeat(character, count))
					if err := decision.Validate(); (err == nil) != (count <= tc.limit) {
						t.Errorf("%q repeated %d: err=%v, character limit=%d", character, count, err, tc.limit)
					}
				}
			}
			// JSON Schema counts Unicode code points, including each combining mark.
			if err := tc.make(strings.Repeat("e\u0301", tc.limit/2)).Validate(); err != nil {
				t.Errorf("combining characters within the bound rejected: %v", err)
			}
			if err := tc.make(strings.Repeat("e\u0301", tc.limit/2) + "x").Validate(); err == nil {
				t.Error("combining characters beyond the bound accepted")
			}
		})
	}
}
