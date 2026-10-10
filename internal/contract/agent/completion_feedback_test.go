package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompletionFeedbackDecisionContract(t *testing.T) {
	for _, tc := range []struct {
		name     string
		feedback any
		want     bool
	}{
		{"complete", "", true},
		{"missing work", "The requested independent read is missing.", true},
		{"unicode boundary", strings.Repeat("读", 2000), true},
		{"over boundary", strings.Repeat("读", 2001), false},
		{"null", nil, false},
		{"wrong type", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(JSON{"kind": "final", "answer_markdown": "draft", "fact_ids": []string{}, "completion_feedback": tc.feedback})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := StrictDecision(raw); (err == nil) != tc.want {
				t.Fatalf("feedback contract accepted=%v, want=%v: %v", err == nil, tc.want, err)
			}
		})
	}
	if _, err := StrictDecision([]byte(`{"kind":"final","answer_markdown":"legacy","fact_ids":[]}`)); err != nil {
		t.Fatal("optional feedback broke existing final decisions", err)
	}
	if _, err := StrictDecision([]byte(`{"kind":"inspect_capability","name":"records.read","completion_feedback":"missing"}`)); err == nil {
		t.Fatal("feedback accepted on a non-final decision")
	}
}
