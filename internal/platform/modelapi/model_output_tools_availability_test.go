package modelapi

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/ext/capability"
)

func TestOutputToolsSkillGuidanceMatchesAvailableDecisions(t *testing.T) {
	const hostRule = "Read page context before each host action. Do not repeat a successful write."
	prompt := capability.AgentPrompt(hostRule)
	for _, tc := range []struct {
		name   string
		skills []agentcontract.JSON
	}{
		{name: "absent"},
		{name: "empty", skills: []agentcontract.JSON{}},
		{name: "declared", skills: []agentcontract.JSON{{"name": "pack.guide"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &HTTPJSONDecisionModel{Model: "test", DecisionOutputMode: "output_tools"}
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Skills: tc.skills, CapabilityCatalogTotal: 20}
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
			wantSkills := len(tc.skills) > 0
			if strings.Contains(got, "tool_batch, read_skill,") != wantSkills || strings.Contains(got, "Read the relevant skill before") != wantSkills || (outputDecisionSchemas(t, payload)["read_skill"] != nil) != wantSkills {
				t.Fatal("skill usage instructions and output schema disagree about current availability")
			}
			if !wantSkills && !strings.Contains(got, "read_skill is unavailable") {
				t.Fatal("unavailable skill guidance is missing")
			}
			for _, rule := range []string{hostRule, "Call submit_decision exactly once", "runtime separately validates and authorizes capability calls", "tool_batch: required", "inspect_capability: required", "final: required"} {
				if !strings.Contains(got, rule) {
					t.Fatalf("required protocol or host rule missing: %q", rule)
				}
			}
			assertSingleDecisionOutputRequest(t, payload)
			if messages[1].(agentcontract.JSON)["content"] != string(input) {
				t.Fatal("skill availability changed cumulative evidence")
			}
			raw, err := agentcontract.CanonicalJSON(payload)
			if err != nil {
				t.Fatal(err)
			}
			measurement, err := model.MeasureInput(packet, prompt)
			if err != nil || measurement.Tokens != int64(len(raw)+128) {
				t.Fatal("measurement and actual request differ", measurement, err)
			}
			after, err := agentcontract.CanonicalJSON(packet)
			if err != nil || string(after) != string(input) {
				t.Fatal("skill availability mutated caller context")
			}
		})
	}
}

func TestDecisionOutputToolsSearchMatchesRuntimeFeature(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features []string
		want     bool
	}{
		{name: "disabled"},
		{name: "durable execution only", features: []string{"durable_execution"}},
		{name: "enabled with deferred catalog", features: []string{"capability_search"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &HTTPJSONDecisionModel{Model: "test", APIType: "compatible_chat", DecisionOutputMode: "output_tools"}
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", RuntimeFeatures: tc.features, CapabilityCatalogTotal: 20}
			raw, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := model.requestPayload(raw, "Choose one decision at a time.")
			if err != nil {
				t.Fatal(err)
			}
			schemas := outputDecisionSchemas(t, payload)
			prompt := payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
			if (schemas["search_capabilities"] != nil) != tc.want || strings.Contains(prompt, "search_capabilities: required") != tc.want {
				t.Fatalf("search tool and guidance do not match runtime availability: kinds=%v, want=%v", schemas, tc.want)
			}
			for _, name := range []string{"tool_batch", "final", "request_input", "inspect_capability", "inspect_fact"} {
				if schemas[name] == nil {
					t.Fatalf("unrelated output decision was removed: %s", name)
				}
			}
			assertSingleDecisionOutputRequest(t, payload)
		})
	}
}

func TestCompletionReviewOffersOnlyFinalOutput(t *testing.T) {
	model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: "output_tools"}
	packet := agentcontract.ContextPacket{
		Schema: "agenstra.context.v1", RuntimeFeatures: []string{"capability_search"},
		Skills:           []agentcontract.JSON{{"name": "existing-skill"}},
		CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "proposed", FactIDs: []string{}},
		Observations:     []agentcontract.Observation{{CallRef: "read-1", Capability: "records.read", Arguments: agentcontract.JSON{"id": 23}, Status: "succeeded"}},
	}
	before, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := model.requestPayload(before, "Review the final proposal.")
	if err != nil {
		t.Fatal(err)
	}
	schemas := outputDecisionSchemas(t, payload)
	if len(schemas) != 2 || schemas["final"] == nil || schemas["approve_completion"] == nil {
		t.Fatal("completion review offered decisions that runtime cannot accept")
	}
	properties := schemas["final"]["properties"].(agentcontract.JSON)
	feedback := properties["completion_feedback"].(agentcontract.JSON)
	if feedback["type"] != "string" || feedback["maxLength"] != 2000 {
		t.Fatal("review feedback contract is missing its type or bound", feedback)
	}
	messages := payload["messages"].([]any)
	prompt := messages[0].(agentcontract.JSON)["content"].(string)
	if strings.Contains(prompt, "choose kind tool_batch") || strings.Contains(prompt, "choose one inspection and wait") {
		t.Fatal("completion review still instructs the model to choose unavailable decisions")
	}
	for _, tool := range decisionOutputTools(false) {
		name := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)["name"].(string)
		kind := strings.TrimPrefix(name, "submit_")
		if strings.Contains(prompt, kind+": required") != (kind == "final") {
			t.Fatalf("output field guidance offers unavailable review decision %s", name)
		}
	}
	assertReviewProposalInputPreserved(t, payload, before)
	assertSingleDecisionOutputRequest(t, payload)
	raw, err := agentcontract.CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := model.MeasureInput(packet, "Review the final proposal.")
	if err != nil || measurement.Tokens != int64(len(raw)+128) {
		t.Fatalf("review request and measurement differ: %+v %v", measurement, err)
	}
	after, err := agentcontract.CanonicalJSON(packet)
	if err != nil || string(before) != string(after) {
		t.Fatal("review transport mutated caller evidence")
	}
	model.DecisionOutputMode = "json_object"
	legacy, err := model.requestPayload(before, "Review the final proposal.")
	if err != nil || legacy["tools"] != nil || legacy["response_format"] == nil {
		t.Fatal("review transport changed content-only JSON")
	}
	jsonPrompt := legacy["messages"].([]any)[0].(agentcontract.JSON)["content"].(string)
	if !strings.Contains(jsonPrompt, "final or approve_completion only") || !strings.Contains(jsonPrompt, "completion_feedback is REQUIRED") {
		t.Fatal("JSON review did not advertise its actual stage contract", jsonPrompt)
	}
}

func TestJSONReviewRemovesOrdinaryDecisionExamples(t *testing.T) {
	prompt := "Keep host evidence rules.\nChoose one JSON decision at a time: tool_batch, read_skill, inspect_capability, inspect_fact, final, or request_input.\nReturn one JSON object with schema and an ordinary kind.\nSkill: {\"kind\":\"read_skill\"}\n" + agentcontract.DecisionObjectShapePrompt
	got := jsonCompletionReviewPrompt(prompt)
	if !strings.Contains(got, "Keep host evidence rules.") || strings.Contains(got, "Choose one JSON decision at a time:") || strings.Contains(got, "Skill: {") || strings.Contains(got, agentcontract.DecisionObjectShapePrompt) {
		t.Fatal("review retained contradictory examples or removed host rules", got)
	}
	model := &HTTPJSONDecisionModel{Model: "test", DecisionOutputMode: "json_object"}
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", CompletionReview: &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft"}}
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := model.requestPayload(raw, prompt)
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := agentcontract.CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := model.MeasureInput(packet, prompt)
	if err != nil || measurement.Tokens != int64(len(serialized)+128) {
		t.Fatal("JSON review guidance was omitted from input measurement", measurement, err)
	}
}
