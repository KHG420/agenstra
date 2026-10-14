package agent

import (
	"encoding/json"
	"testing"
)

func TestCompactCompletionApprovalContract(t *testing.T) {
	if _, err := StrictDecision([]byte(`{"schema":"agenstra.decision.v1","kind":"approve_completion"}`)); err != nil {
		t.Fatal("compact verdict rejected", err)
	}
	for _, field := range []string{"answer_markdown", "fact_ids", "result_refs", "completion_feedback", "calls", "name", "path", "offset", "execute"} {
		raw, err := json.Marshal(JSON{"kind": "approve_completion", field: nil})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := StrictDecision(raw); err == nil {
			t.Fatal("verdict accepted payload", field)
		}
	}
	for _, d := range []Decision{
		{Kind: "approve_completion", AnswerMarkdown: "replacement"},
		{Kind: "approve_completion", FactIDs: []string{NewID()}},
		{Kind: "approve_completion", CompletionFeedback: "missing work"},
		{Kind: "approve_completion", Calls: []ToolCall{{Capability: "records.create"}}},
	} {
		if err := d.Validate(); err == nil {
			t.Fatal("custom model verdict accepted payload", d)
		}
	}
}
