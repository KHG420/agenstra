package agenstra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/jsonvalue"
)

func memoryModelReply(t *testing.T, w http.ResponseWriter, content string) {
	if callErr := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": content}}}}); callErr != nil {
		t.Error(callErr)
	}
}

func TestMemoryHTTPJSONExtractorContractAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"empty", `{"proposals":[]}`, true},
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
				if b.Messages[0].Content != memoryExtractionPrompt || utf8.RuneCountInString(b.Messages[0].Content)+utf8.RuneCountInString(b.Messages[1].Content) > 3000 {
					t.Error("extraction input exceeded budget")
				}
				var input MemoryExtractionRequest
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
			request := MemoryExtractionRequest{Text: "用中文写报告", MaxCharacters: 3000}
			for i := 0; i < 20; i++ {
				request.Existing = append(request.Existing, MemoryView{Key: fmt.Sprintf("topic.%d", i), Value: strings.Repeat("偏好", 200)})
			}
			_, err = model.ExtractMemories(t.Context(), request)
			if tc.valid && err != nil || !tc.valid && ErrorCode(err) != "memory_extraction_invalid" {
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
	_, err := model.ExtractMemories(t.Context(), MemoryExtractionRequest{Text: strings.Repeat("过长", 2000), MaxCharacters: 3000})
	if ErrorCode(err) != "memory_extraction_too_large" || requests.Load() != 0 {
		t.Fatal("oversized extraction performed IO", err, requests.Load())
	}
}

func TestMemoryHTTPJSONModelAutomaticallyLearnsAndRecallsAcrossRuns(t *testing.T) {
	var extraction, decisions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Messages []struct{ Content string } }
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.Messages) != 2 {
			t.Error(b, err)
			return
		}
		if b.Messages[0].Content == memoryExtractionPrompt {
			extraction.Add(1)
			var input MemoryExtractionRequest
			if err := jsonvalue.DecodeStrict([]byte(b.Messages[1].Content), &input); err != nil {
				t.Error(err)
				return
			}
			proposals := []MemoryProposal{}
			if strings.Contains(input.Text, "中文") {
				proposals = memoryProposal(input, "user", "report.language", "zh-CN", "habit")
			}
			content, callErr3 := CanonicalJSON(JSON{"proposals": proposals})
			if callErr3 != nil {
				t.Error(callErr3)
			}
			memoryModelReply(t, w, string(content))
			return
		}
		n := decisions.Add(1)
		var packet ContextPacket
		if err := jsonvalue.DecodeStrict([]byte(b.Messages[1].Content), &packet); err != nil {
			t.Error(err)
			return
		}
		if n < 3 && len(packet.Memories) != 0 || n >= 3 && (len(packet.Memories) != 1 || packet.Memories[0].Value != "zh-CN") {
			t.Error("incorrect cross-run projection", n, packet.Memories)
		}
		if !strings.Contains(b.Messages[0].Content, conversationGuidance) {
			t.Error("conversation instructions missing")
		}
		if n >= 3 && !strings.Contains(b.Messages[0].Content, memoryUsagePrompt) {
			t.Error("memory precedence instructions missing")
		}
		memoryModelReply(t, w, `{"schema":"agenstra.decision.v1","kind":"final","answer_markdown":"完成","fact_ids":[]}`)
	}))
	defer server.Close()
	model, callErr4 := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
	if callErr4 != nil {
		t.Error(callErr4)
	}
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	h.Model = model
	for i := 0; i < 3; i++ {
		memoryRun(t, h, "alice", "records", fmt.Sprintf("请用中文写第%d份报告", i), fmt.Sprint(i))
	}
	memoryRun(t, h, "alice", "records", "读取新的记录", "recall")
	if extraction.Load() != 4 || decisions.Load() != 4 {
		t.Fatal(extraction.Load(), decisions.Load())
	}
}

func TestMemoryHostSkipsOversizedLearningAndContinuesBusinessRun(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(MemoryExtractionRequest) ([]MemoryProposal, error) {
		t.Fatal("oversized source reached extractor")
		return nil, nil
	}}
	// This case needs no business capability; keep its catalog empty so the
	// business context fits while the extraction prompt exceeds the limit.
	h := testHost(t, testStore(t), &hostProvider{caps: map[string]CapabilityDescription{}}, model.hostModel)
	h.Model = model
	// Independently size the source and business request. Fixed protocol guidance
	// must not determine which of these two inputs exceeds the shared budget.
	runtime := &AgentRuntime{Provider: &hostProvider{caps: map[string]CapabilityDescription{}}}
	h.Settings.MaxContextCharacters = utf8.RuneCountInString(runtime.systemPrompt()) + 1000
	create := func() StoredRun {
		t.Helper()
		run, err := h.createWithMemoryInput(t.Context(), "alice", "records", "请用中文写报告", "budget", strings.Repeat("偏好", h.Settings.MaxContextCharacters))
		if err != nil {
			t.Fatal(err)
		}
		run, err = h.Drive(t.Context(), run.RunID, "alice")
		if err != nil || run.Status != "completed" {
			t.Fatalf("run: %s %v", run.Status, err)
		}
		return run
	}
	run := create()
	errors, ok := run.State["memory_errors"].([]any)
	if !ok || len(errors) != 1 || errors[0].(map[string]any)["code"] != "memory_extraction_too_large" || len(model.inputs) != 0 {
		t.Fatal("learning budget failure not exposed", run.State)
	}
	// No independent source arrives on resume: failed learning is idempotent too.
	create()
}

func TestMemoryLostLeaseCannotCommitExtraction(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{}}
	h, store := memoryTestHost(t, model)
	run, err := h.Create(t.Context(), "alice", "records", "请用中文写报告", "fence")
	if err != nil {
		t.Fatal(err)
	}
	model.extract = func(r MemoryExtractionRequest) ([]MemoryProposal, error) {
		// Simulate a cancellation taking the lease while extraction is in flight.
		if _, err := store.DB.Exec("UPDATE runs SET lease_token=NULL WHERE run_id=?", run.RunID); err != nil {
			t.Fatal(err)
		}
		return memoryProposal(r, "user", "report.language", "zh-CN", "explicit"), nil
	}
	_, err = h.Drive(context.Background(), run.RunID, "alice")
	if err == nil {
		t.Fatal("lease loss went unnoticed")
	}
	items, err := h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if err != nil || len(items) != 0 || model.calls != 0 {
		t.Fatal("lost lease committed learning or called business model", items, err, model.calls)
	}
}
