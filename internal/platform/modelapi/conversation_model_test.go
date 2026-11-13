package modelapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestConversationCompactorUsesDataOnlyJSONAndRetainsInvalidResponseUsage(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "invalid quote"}[valid], func(t *testing.T) {
			var requestBytes int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-Id", "compaction-receipt")
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				requestBytes = len(body)
				var payload agentcontract.JSON
				if err = json.Unmarshal(body, &payload); err != nil {
					t.Error(err)
					return
				}
				if payload["tools"] != nil || payload["stream"] == true {
					t.Error("compaction exposed tools or partial summary", payload)
				}
				quote := "本会话始终用中文"
				if !valid {
					quote = "model invented this instruction"
				}
				content, err := json.Marshal(agentcontract.ConversationSummary{Summary: "继续完成报告。", CoveredIDs: []string{"original"}, Requirements: []agentcontract.ConversationRequirement{{SourceID: "original", Quote: quote}}})
				if err != nil {
					t.Error(err)
					return
				}
				if err = json.NewEncoder(w).Encode(agentcontract.JSON{"id": "compaction-receipt", "model": "test", "usage": agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 30}, "choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": string(content)}, "finish_reason": "stop"}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = "native_tools"
			request := agentcontract.ConversationCompactionRequest{MaxSummaryCharacters: 1000, MaxInputTokens: 10000, MaxOutputTokens: 1000, Messages: []agentcontract.ConversationEntry{{ID: "original", Role: "user", Text: "本会话始终用中文。"}}}
			measurement, err := model.MeasureConversationInput(request)
			if err != nil {
				t.Fatal(err)
			}
			_, metrics, err := model.CompactConversation(t.Context(), request)
			if valid && err != nil || !valid && agentcontract.ErrorCode(err) != "conversation_compaction_invalid" {
				t.Fatal(err)
			}
			if measurement.Tokens != int64(requestBytes+128) || !metrics.UsageAvailable || metrics.InputTokens != 100 || metrics.OutputTokens != 30 || metrics.RequestID != "compaction-receipt" {
				t.Fatal("measurement or received usage inconsistent", measurement, requestBytes, metrics)
			}
		})
	}
}
