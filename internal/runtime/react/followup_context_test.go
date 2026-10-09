package react

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestFollowupExecutionOrderingSeparatesReadsBeforeAndAfterReply(t *testing.T) {
	state := &agentcontract.RuntimeState{Followups: []string{"label: supplied"}}
	recordDecision(state, agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "before-reply"}}})
	recordDecision(state, agentcontract.Decision{Kind: "request_input", Field: "label"})
	before, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	prompt := followupExecutionPrompt(state)
	if !strings.Contains(prompt, `"after_reply_call_refs":[]`) || strings.Contains(prompt, "before-reply") {
		t.Fatal("a prior read was presented as post-reply verification", prompt)
	}
	after, err := agentcontract.CanonicalJSON(state)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("ordering projection changed the saved run")
	}
	recordDecision(state, agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "after-reply"}, {CallRef: "before-reply"}}})
	prompt = followupExecutionPrompt(state)
	if !strings.Contains(prompt, `"after_reply_call_refs":["after-reply"]`) || strings.Contains(prompt, "before-reply") {
		t.Fatal("a reused reference retimed the original result", prompt)
	}

	// A second request for the same field introduces a new execution boundary.
	recordDecision(state, agentcontract.Decision{Kind: "request_input", Field: "label"})
	state.Followups = append(state.Followups, "label: second reply")
	if prompt = followupExecutionPrompt(state); !strings.Contains(prompt, `"after_reply_call_refs":[]`) {
		t.Fatal("the first reply's read satisfied the second reply's verification", prompt)
	}
}

func TestFollowupExecutionOrderingDoesNotGuessMissingOrSteeredInput(t *testing.T) {
	for _, state := range []*agentcontract.RuntimeState{
		{Decisions: []agentcontract.JSON{{"kind": "request_input", "field": "label"}}},
		{Followups: []string{"other: supplied"}, Decisions: []agentcontract.JSON{{"kind": "request_input", "field": "label"}}},
		{Followups: []string{"label: supplied"}, InputField: agentcontract.Strptr("label"), Decisions: []agentcontract.JSON{{"kind": "request_input", "field": "label"}}},
		{Followups: []string{"label: supplied"}, SteeringCursor: 1, Decisions: []agentcontract.JSON{{"kind": "request_input", "field": "label"}}},
		{Followups: []string{"label: supplied", "steering: read again"}, Decisions: []agentcontract.JSON{{"kind": "request_input", "field": "label"}}},
	} {
		if prompt := followupExecutionPrompt(state); prompt != "" {
			t.Fatal("ordering was guessed without a reliable input boundary", prompt)
		}
	}
}

func TestFollowupExecutionOrderingBoundsReferencesWithoutHidingOmissions(t *testing.T) {
	state := &agentcontract.RuntimeState{Followups: []string{"label: supplied"}}
	recordDecision(state, agentcontract.Decision{Kind: "request_input", Field: "label"})
	for i := 0; i < 15; i++ {
		recordDecision(state, agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: fmt.Sprintf("call-%02d", i)}}})
	}
	prompt := followupExecutionPrompt(state)
	if !strings.Contains(prompt, `"omitted_after_reply_call_refs":3`) || strings.Contains(prompt, "call-00") || !strings.Contains(prompt, "call-14") {
		t.Fatal("older ordering references were not bounded and disclosed", prompt)
	}
}
