package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/ext/capability"
)

func TestJSONDecisionOmitsUnavailableSkillGuidance(t *testing.T) {
	const hostRule = "Read page context before each host action. Do not repeat a successful write."
	prompt := capability.AgentPrompt(hostRule)
	for _, mode := range []string{"", "json_object"} {
		t.Run(mode, func(t *testing.T) {
			model := &HTTPJSONDecisionModel{Model: "test", DecisionOutputMode: mode}
			packet := agentcontract.ContextPacket{
				Schema: "agenstra.context.v1", RuntimeFeatures: []string{"capability_search"},
				CapabilityCatalogTotal: 20,
				Capabilities:           []agentcontract.JSON{{"name": "page.context", "skills": []any{}}},
			}
			input, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := model.requestPayload(input, prompt)
			if err != nil {
				t.Fatal(err)
			}
			messages := payload["messages"].([]any)
			got := messages[0].(agentcontract.JSON)["content"].(string)
			if strings.Contains(got, `"kind":"read_skill"`) || strings.Contains(got, "tool_batch, read_skill,") || strings.Contains(got, "Read the relevant skill before") {
				t.Fatal("JSON transport advertises an unavailable skill decision")
			}
			for _, rule := range []string{hostRule, "read_skill is unavailable", "Capability names identify callable operations, never skills", `"kind":"tool_batch"`, `"kind":"inspect_capability"`, `"kind":"final"`} {
				if !strings.Contains(got, rule) {
					t.Fatalf("required protocol or host rule missing: %q", rule)
				}
			}
			if messages[1].(agentcontract.JSON)["content"] != string(input) || payload["tools"] != nil || payload["response_format"] == nil {
				t.Fatal("JSON availability changed the packet or output channel")
			}
			raw, err := agentcontract.CanonicalJSON(payload)
			if err != nil {
				t.Fatal(err)
			}
			measurement, err := model.MeasureInput(packet, prompt)
			if err != nil || measurement.Tokens != int64(len(raw)+128) {
				t.Fatalf("measurement omitted dynamic availability: %+v %v", measurement, err)
			}
			after, err := agentcontract.CanonicalJSON(packet)
			if err != nil || string(after) != string(input) {
				t.Fatal("availability transport mutated the caller's packet")
			}
		})
	}
}

func TestJSONAvailabilityPreservesExistingSkillsAndMemory(t *testing.T) {
	prompt := capability.AgentPrompt("Preserve the host rule.")
	input, err := agentcontract.CanonicalJSON(agentcontract.ContextPacket{Schema: "agenstra.context.v1", Skills: []agentcontract.JSON{{"name": "existing-skill"}}})
	if err != nil {
		t.Fatal(err)
	}
	review, err := agentcontract.CanonicalJSON(agentcontract.ContextPacket{Schema: "agenstra.context.v1", CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "proposed", FactIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	model := &HTTPJSONDecisionModel{Model: "test", DecisionOutputMode: "json_object"}
	for _, tc := range []struct {
		raw      []byte
		prompt   string
		decision bool
	}{
		{input, prompt, true},
		{review, jsonCompletionReviewPrompt(prompt), true},
		{[]byte(`{"text":"remember my preference","existing":[]}`), prompt, false},
	} {
		payload, err := model.requestPayload(tc.raw, prompt)
		if err != nil {
			t.Fatal(err)
		}
		messages := payload["messages"].([]any)
		got := messages[0].(agentcontract.JSON)["content"].(string)
		if tc.decision {
			if !strings.HasPrefix(got, tc.prompt+"\n\n") || !strings.Contains(got, "apply to final.answer_markdown only") {
				t.Fatal("decision prompt lost existing phase guidance or display boundary")
			}
		} else if got != tc.prompt {
			t.Fatal("decision display guidance changed a memory request")
		}
		if messages[1].(agentcontract.JSON)["content"] != string(tc.raw) {
			t.Fatal("request prompt changed outside its phase or caller context was mutated")
		}
	}
	const customPrompt = "Return the host's documented decision object."
	payload, err := model.requestPayload([]byte(`{"schema":"agenstra.context.v1","skills":[]}`), customPrompt)
	if err != nil {
		t.Fatal(err)
	}
	got := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
	if !strings.HasPrefix(got, customPrompt+"\n\n") || !strings.Contains(got, "apply to final.answer_markdown only") {
		t.Fatal("transport lost a custom decision prompt or display boundary")
	}
}
