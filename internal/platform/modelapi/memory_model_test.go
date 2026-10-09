package modelapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func memoryModelReply(t *testing.T, w http.ResponseWriter, content string) {
	if callErr := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": content}}}}); callErr != nil {
		t.Error(callErr)
	}
}

func TestMemoryHTTPJSONExtractorContractAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"empty", `{"proposals":[]}`, true},
		{"fenced", "```json\n{\"proposals\":[]}\n```", true},
		{"fenced invalid schema", "```json\n{\"proposals\":[],\"owner\":\"bob\"}\n```", false},
		{"duplicate", `{"proposals":[],"proposals":[]}`, false},
		{"nested duplicate", `{"proposals":[{"scope":"user","key":"report.language","value":"en","value":"zh-CN","kind":"preference","mode":"habit","quote":"用中文写报告"}]}`, false},
		{"habit", `{"proposals":[{"scope":"user","key":"report.language","value":"zh-CN","kind":"preference","mode":"habit","quote":"用中文写报告"}]}`, true},
		{"missing", `{}`, false},
		{"null", `{"proposals":null}`, false},
		{"trailing", `{"proposals":[]} {}`, false},
		{"identity", `{"proposals":[],"owner":"bob"}`, false},
		{"invented", `{"proposals":[{"scope":"user","key":"report.language","value":"zh-CN","kind":"preference","mode":"habit","quote":"from assistant history"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("model transport contract changed")
				}
				var b struct {
					Messages []struct{ Role, Content string }
				}
				if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.Messages) != 2 {
					t.Error(b, err)
					return
				}
				if b.Messages[0].Content != agentcontract.MemoryExtractionPrompt || utf8.RuneCountInString(b.Messages[0].Content)+utf8.RuneCountInString(b.Messages[1].Content) > 3000 {
					t.Error("extraction input exceeded budget")
				}
				var input agentcontract.MemoryExtractionRequest
				if err := jsonvalue.DecodeStrict([]byte(b.Messages[1].Content), &input); err != nil || input.Text != "用中文写报告" || len(input.Existing) >= 20 {
					t.Error("full source lost or optional memories not trimmed", input, err)
				}
				memoryModelReply(t, w, tc.content)
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			request := agentcontract.MemoryExtractionRequest{Text: "用中文写报告", MaxCharacters: 3000}
			for i := 0; i < 20; i++ {
				request.Existing = append(request.Existing, agentcontract.MemoryView{Key: fmt.Sprintf("topic.%d", i), Value: strings.Repeat("偏好", 200)})
			}
			_, err = model.ExtractMemories(t.Context(), request)
			if tc.valid && err != nil || !tc.valid && agentcontract.ErrorCode(err) != "memory_extraction_invalid" {
				t.Fatal(err)
			}
		})
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	model, callErr2 := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
	if callErr2 != nil {
		t.Error(callErr2)
	}
	_, err := model.ExtractMemories(t.Context(), agentcontract.MemoryExtractionRequest{Text: strings.Repeat("过长", 2000), MaxCharacters: 3000})
	if agentcontract.ErrorCode(err) != "memory_extraction_too_large" || requests.Load() != 0 {
		t.Fatal("oversized extraction performed IO", err, requests.Load())
	}
}
