package modelapi

import (
	"bytes"
	"strings"
	"testing"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/ext/capability"
)

func TestLoadedSkillsAreRetainedCompletedResults(t *testing.T) {
	for _, mode := range []string{"", "json_object", "json_schema", "output_tools"} {
		for _, stage := range []string{"before_business", "business_result", "inspection_history", "completion_review"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				const skillText = "retained-guidance-sentinel; apply only to records.lookup"
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Read the guide, then look up the record.",
					Skills:           []agentcontract.JSON{{"name": "source::$project"}, {"name": "omitted.guide"}},
					LoadedSkills:     map[string]string{"source::$project": skillText},
					ContextOmissions: []string{"skill: omitted.guide; read_skill to load again"}}
				switch stage {
				case "business_result":
					packet.Observations = []agentcontract.Observation{{CallRef: "lookup-1", Capability: "records.lookup", Arguments: agentcontract.JSON{}, Status: "succeeded"}}
				case "inspection_history":
					packet.InspectionHistory = []agentcontract.JSON{{"fact_id": "fact-1", "path": []any{"data"}, "preview": agentcontract.JSON{"value": "saved detail"}}}
				case "completion_review":
					packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: "proposed result", FactIDs: []string{}}
				}
				before, err := agentcontract.CanonicalJSON(packet)
				if err != nil {
					t.Fatal(err)
				}
				model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
				prompt := capability.AgentPrompt("Host grants and source scopes still apply.")
				payload, err := model.requestPayload(before, prompt)
				if err != nil {
					t.Fatal(err)
				}
				messages := payload["messages"].([]any)
				resultIndex := -1
				for i, value := range messages {
					message := value.(agentcontract.JSON)
					content, _ := message["content"].(string)
					if !strings.HasPrefix(content, "Saved read_skill results from this run;") {
						continue
					}
					if resultIndex != -1 || message["role"] != "user" || !strings.Contains(content, "these reads have already completed") {
						t.Fatal("loaded skill result is duplicated or is not marked as completed")
					}
					_, data, found := strings.Cut(content, "\n")
					var retained map[string]string
					if !found || jsonvalue.DecodeStrict([]byte(data), &retained) != nil || len(retained) != 1 || retained["source::$project"] != skillText {
						t.Fatal("retained skill content or scope changed")
					}
					resultIndex = i
				}
				if resultIndex != 2 {
					t.Fatal("loaded skill has no explicit completed result before later evidence", resultIndex)
				}
				var cumulative agentcontract.ContextPacket
				if err := jsonvalue.DecodeStrict([]byte(messages[1].(agentcontract.JSON)["content"].(string)), &cumulative); err != nil {
					t.Fatal(err)
				}
				if len(cumulative.LoadedSkills) != 0 || cumulative.Instruction != packet.Instruction || len(cumulative.ContextOmissions) != 1 {
					t.Fatal("skill presentation duplicated its contents or changed task/omissions")
				}
				raw, err := agentcontract.CanonicalJSON(payload)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Count(raw, []byte("retained-guidance-sentinel")) != 1 {
					t.Fatal("retained guidance must enter the measured request exactly once")
				}
				measurement, err := model.MeasureInput(packet, prompt)
				if err != nil || measurement.Tokens != int64(len(raw)+128) {
					t.Fatal("completed skill result escaped request input measurement", measurement, err)
				}
				after, err := agentcontract.CanonicalJSON(packet)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("request projection mutated caller-owned skill or evidence state", err)
				}
				if stage == "completion_review" && mode == "output_tools" {
					schemas := outputDecisionSchemas(t, payload)
					if len(schemas) != 2 || schemas["final"] == nil || schemas["approve_completion"] == nil {
						t.Fatal("retained guidance changed completion-review decision availability")
					}
				}
			})
		}
	}
}

func TestNoLoadedSkillsDoesNotInventCompletedReads(t *testing.T) {
	for _, mode := range []string{"json_object", "json_schema", "output_tools"} {
		packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Skills: []agentcontract.JSON{{"name": "unread.guide"}}}
		input, err := agentcontract.CanonicalJSON(packet)
		if err != nil {
			t.Fatal(err)
		}
		model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: mode}
		payload, err := model.requestPayload(input, capability.AgentPrompt("Host rules apply."))
		if err != nil {
			t.Fatal(err)
		}
		messages := payload["messages"].([]any)
		if len(messages) != 2 || messages[1].(agentcontract.JSON)["content"] != string(input) {
			t.Fatal("unread skills were presented as completed or cumulative input changed")
		}
	}
}
