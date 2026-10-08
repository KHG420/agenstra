package agenstra

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestFollowupExecutionOrderingSeparatesReadsBeforeAndAfterReply(t *testing.T) {
	state := &RuntimeState{Followups: []string{"label: supplied"}}
	recordDecision(state, Decision{Kind: "tool_batch", Calls: []ToolCall{{CallRef: "before-reply"}}})
	recordDecision(state, Decision{Kind: "request_input", Field: "label"})
	before, err := CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	prompt := followupExecutionPrompt(state)
	if !strings.Contains(prompt, `"after_reply_call_refs":[]`) || strings.Contains(prompt, "before-reply") {
		t.Fatal("a prior read was presented as post-reply verification", prompt)
	}
	after, err := CanonicalJSON(state)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("ordering projection changed the saved run")
	}
	recordDecision(state, Decision{Kind: "tool_batch", Calls: []ToolCall{{CallRef: "after-reply"}, {CallRef: "before-reply"}}})
	prompt = followupExecutionPrompt(state)
	if !strings.Contains(prompt, `"after_reply_call_refs":["after-reply"]`) || strings.Contains(prompt, "before-reply") {
		t.Fatal("a reused reference retimed the original result", prompt)
	}
	// A second request for the same field introduces a new execution boundary.
	recordDecision(state, Decision{Kind: "request_input", Field: "label"})
	state.Followups = append(state.Followups, "label: second reply")
	if prompt = followupExecutionPrompt(state); !strings.Contains(prompt, `"after_reply_call_refs":[]`) {
		t.Fatal("the first reply's read satisfied the second reply's verification", prompt)
	}
}

func TestFollowupExecutionOrderingDoesNotGuessMissingOrSteeredInput(t *testing.T) {
	for _, state := range []*RuntimeState{
		{Decisions: []JSON{{"kind": "request_input", "field": "label"}}},
		{Followups: []string{"other: supplied"}, Decisions: []JSON{{"kind": "request_input", "field": "label"}}},
		{Followups: []string{"label: supplied"}, InputField: strptr("label"), Decisions: []JSON{{"kind": "request_input", "field": "label"}}},
		{Followups: []string{"label: supplied"}, SteeringCursor: 1, Decisions: []JSON{{"kind": "request_input", "field": "label"}}},
		{Followups: []string{"label: supplied", "steering: read again"}, Decisions: []JSON{{"kind": "request_input", "field": "label"}}},
	} {
		if prompt := followupExecutionPrompt(state); prompt != "" {
			t.Fatal("ordering was guessed without a reliable input boundary", prompt)
		}
	}
}

func TestFollowupExecutionOrderingBoundsReferencesWithoutHidingOmissions(t *testing.T) {
	state := &RuntimeState{Followups: []string{"label: supplied"}}
	recordDecision(state, Decision{Kind: "request_input", Field: "label"})
	for i := 0; i < 15; i++ {
		recordDecision(state, Decision{Kind: "tool_batch", Calls: []ToolCall{{CallRef: fmt.Sprintf("call-%02d", i)}}})
	}
	prompt := followupExecutionPrompt(state)
	if !strings.Contains(prompt, `"omitted_after_reply_call_refs":3`) || strings.Contains(prompt, "call-00") || !strings.Contains(prompt, "call-14") {
		t.Fatal("older ordering references were not bounded and disclosed", prompt)
	}
}

func TestModelFollowupOutcomePreservesFailureAndProjectedEvidence(t *testing.T) {
	model := &HTTPJSONDecisionModel{Model: "fake"}
	packet := ContextPacket{Schema: "agenstra.context.v1", Followups: []string{"label: supplied"},
		Observations: []Observation{{CallRef: "read-1", Capability: "record.read", Status: "failed", ErrorCode: strptr("host_request_rejected"), Arguments: JSON{"id": 23}, FactID: strptr("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}},
		Facts:        []FactView{{Fact: Fact{FactID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Value: JSON{"visible": "projected only"}}, OmittedPaths: [][]any{{"hidden"}}}},
	}
	input, err := CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := model.requestPayload(input, "Return JSON")
	if err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	content := messages[3].(JSON)["content"].(string)
	if !strings.Contains(content, `"status":"failed"`) || !strings.Contains(content, "host_request_rejected") || !strings.Contains(content, `"omitted_paths":[["hidden"]]`) {
		t.Fatal("the recent outcome hid a failure or an incomplete preview", content)
	}
	for _, change := range []func(){
		func() { packet.Followups = nil },
		func() { packet.Followups = []string{"steering: read again"} },
		func() { packet.Followups = []string{"label: supplied"}; packet.Observations[0].ArgumentsOmitted = true },
		func() {
			packet.Observations[0].ArgumentsOmitted = false
			packet.Observations[0].Capability = "agent.final"
		},
		func() {
			packet.Schema = "agenstra.memory-extraction.v1"
			packet.Observations[0].Capability = "record.read"
		},
	} {
		change()
		input, err = CanonicalJSON(packet)
		if err != nil {
			t.Fatal(err)
		}
		payload, err = model.requestPayload(input, "Return JSON")
		if err != nil || len(payload["messages"].([]any)) != 2 {
			t.Fatal("extra execution messages were added to an ineligible context", err)
		}
	}
}
