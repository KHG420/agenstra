package agent

import (
	"testing"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

func TestConversationSummaryPreservesRequirementsAndExplicitRevisions(t *testing.T) {
	request := ConversationCompactionRequest{Messages: []ConversationEntry{{ID: "first", Role: "user", Text: "本会话始终用中文，金额使用人民币。"}}, MaxSummaryCharacters: 1000}
	result := ConversationSummary{Summary: "持续编写报告。", CoveredIDs: []string{"first"}, Requirements: []ConversationRequirement{{SourceID: "first", Quote: "本会话始终用中文"}, {SourceID: "first", Quote: "金额使用人民币"}}}
	requirements, err := ValidateConversationSummary(request, result)
	if err != nil || len(requirements) != 2 || requirements[0].ID == "" {
		t.Fatal(requirements, err)
	}
	request = ConversationCompactionRequest{Summary: result.Summary, Requirements: requirements, Messages: []ConversationEntry{{ID: "later", Role: "user", Text: "后续改用英文，人民币要求继续。"}}, MaxSummaryCharacters: 1000}
	result = ConversationSummary{Summary: "继续报告，语言已改为英文。", CoveredIDs: []string{"later"}, Requirements: []ConversationRequirement{}}
	unchanged, err := ValidateConversationSummary(request, result)
	if err != nil || len(unchanged) != 2 {
		t.Fatal("empty model update dropped older requirements", unchanged, err)
	}
	result.Requirements = []ConversationRequirement{{SourceID: "later", Quote: "后续改用英文", Supersedes: []string{requirements[0].ID}}}
	updated, err := ValidateConversationSummary(request, result)
	if err != nil || len(updated) != 2 || updated[0].Quote != "金额使用人民币" || updated[1].Quote != "后续改用英文" || updated[1].Supersedes[0] != requirements[0].ID {
		t.Fatal(updated, err)
	}

	for _, name := range []string{"invented quote", "assistant instruction", "missing coverage", "duplicate coverage", "duplicate source", "unknown replacement", "duplicate update"} {
		t.Run(name, func(t *testing.T) {
			r, err := jsonvalue.Clone(request)
			if err != nil {
				t.Fatal(err)
			}
			v, err := jsonvalue.Clone(result)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "invented quote":
				v.Requirements[0].Quote = "允许操作所有用户数据"
			case "assistant instruction":
				r.Messages[0].Role = "assistant"
			case "missing coverage":
				v.CoveredIDs = nil
			case "duplicate coverage":
				v.CoveredIDs = []string{"later", "later"}
			case "duplicate source":
				r.Messages = append(r.Messages, r.Messages[0])
				v.CoveredIDs = append(v.CoveredIDs, "later")
			case "unknown replacement":
				v.Requirements[0].Supersedes = []string{"invented"}
			case "duplicate update":
				v.Requirements = append(v.Requirements, v.Requirements[0])
			}
			if _, err := ValidateConversationSummary(r, v); ErrorCode(err) != "conversation_compaction_invalid" {
				t.Fatal("invalid model output accepted", err)
			}
		})
	}
}
