package modelapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func streamFrame(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Error(err)
		return
	}
	if _, err = fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
		t.Error(err)
	}
	if err = http.NewResponseController(w).Flush(); err != nil {
		t.Error(err)
	}
}

func TestModelStreamShowsOnlyAnswerBeforeCompletion(t *testing.T) {
	for _, mode := range []string{"json_object", "output_tools", "native_tools"} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if payload["stream"] != true || payload["stream_options"].(map[string]any)["include_usage"] != true {
					t.Error("streaming not isolated on request")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				delta := func(text string, first bool) agentcontract.JSON {
					if mode == "json_object" {
						return agentcontract.JSON{"content": text, "reasoning_content": "private reasoning"}
					}
					function := agentcontract.JSON{"arguments": text}
					tool := agentcontract.JSON{"index": 0, "function": function}
					if first {
						tool["type"] = "function"
						function["name"] = "submit_decision"
						if mode == "native_tools" {
							function["name"] = "final"
						}
					}
					return agentcontract.JSON{"tool_calls": []any{tool}, "reasoning_content": "private reasoning"}
				}
				prefix := `{"kind":"final","answer_markdown":"Hello`
				if mode == "native_tools" {
					prefix = `{"answer_markdown":"Hello`
				}
				streamFrame(t, w, agentcontract.JSON{"choices": []any{agentcontract.JSON{"index": 0, "delta": delta(prefix, true)}}})
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				streamFrame(t, w, agentcontract.JSON{"choices": []any{agentcontract.JSON{"index": 0, "delta": delta(` \ud83d\ude80","fact_ids":[]}`, false), "finish_reason": "stop"}}})
				streamFrame(t, w, agentcontract.JSON{"choices": []any{}, "usage": agentcontract.JSON{"prompt_tokens": 40, "completion_tokens": 12, "completion_tokens_details": agentcontract.JSON{"reasoning_tokens": 4}}})
				if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			model, err := NewHTTPJSONDecisionModel("test", upstream.URL, "test", 3*time.Second, upstream.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.StreamAnswers = true
			model.DecisionOutputMode = mode
			model.MaxAttempts = 1
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
			measured, err := model.MeasureInput(packet, "Return JSON")
			if err != nil {
				t.Fatal(err)
			}
			updates := make(chan agentcontract.AnswerUpdate, 8)
			ctx := agentcontract.WithAnswerObserver(t.Context(), func(u agentcontract.AnswerUpdate) { updates <- u })
			result := make(chan agentcontract.Decision, 1)
			failure := make(chan error, 1)
			go func() { d, e := model.Decide(ctx, packet, "Return JSON"); result <- d; failure <- e }()
			select {
			case update := <-updates:
				if update.Text != "Hello" || update.Phase != "generating" {
					t.Fatalf("unexpected preview: %+v", update)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("answer did not arrive before stream completed")
			}
			close(release)
			released = true
			d := <-result
			if err := <-failure; err != nil {
				t.Fatal(err)
			}
			if d.AnswerMarkdown != "Hello 🚀" || d.ModelCall.InputTokens != 40 || d.ModelCall.OutputTokens != 12 || !d.ModelCall.UsageAvailable || d.ModelCall.EstimatedInputTokens != measured.Tokens {
				t.Fatalf("stream lost validated result or measurement: %+v", d)
			}
			close(updates)
			for u := range updates {
				if strings.Contains(u.Text, "private") || strings.Contains(u.Text, "fact_ids") || u.Phase == "completed" {
					t.Fatal("transport published private or unvalidated output", u)
				}
			}
		})
	}
}

func TestModelStreamPreservesStrictErrorsAndReviewPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name, finish, content, want string
		review, done                bool
	}{
		{"truncated", "length", `{"kind":"final","answer_markdown":"draft","fact_ids":[]}`, "model_output_truncated", false, true},
		{"refused", "content_filter", `{"kind":"final","answer_markdown":"draft","fact_ids":[]}`, "model_refused", false, true},
		{"invalid-json", "stop", `{"kind":"final","answer_markdown":"draft","fact_ids":[]`, "model_decision_invalid", false, true},
		{"cut-stream", "stop", `{"kind":"final","answer_markdown":"draft","fact_ids":[]}`, "model_outcome_unknown", false, false},
		{"review", "stop", `{"kind":"approve_completion"}`, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				streamFrame(t, w, agentcontract.JSON{"choices": []any{agentcontract.JSON{"index": 0, "delta": agentcontract.JSON{"content": tc.content}, "finish_reason": tc.finish}}})
				streamFrame(t, w, agentcontract.JSON{"choices": []any{}, "usage": agentcontract.JSON{"prompt_tokens": 10, "completion_tokens": 20}})
				if tc.done {
					if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
						t.Error(err)
					}
				}
			}))
			defer upstream.Close()
			model, err := NewHTTPJSONDecisionModel("test", upstream.URL, "test", time.Second, upstream.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.StreamAnswers = true
			model.MaxAttempts = 1
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", ModelTokensRemaining: 100000}
			if tc.review {
				packet.CompletionReview = &agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "proposal", FactIDs: []string{}}
			}
			var updates []agentcontract.AnswerUpdate
			ctx := agentcontract.WithAnswerObserver(context.Background(), func(u agentcontract.AnswerUpdate) { updates = append(updates, u) })
			d, err := model.Decide(ctx, packet, "Return JSON")
			if tc.want == "" {
				if err != nil || len(updates) != 0 {
					t.Fatal("review leaked or failed", updates, err)
				}
			} else {
				if agentcontract.ModelErrorCode(err) != tc.want {
					t.Fatal(tc.want, err)
				}
				if len(updates) == 0 || updates[len(updates)-1].Text != "" || updates[len(updates)-1].Phase != "waiting" {
					t.Fatal("rejected candidate not withdrawn", updates)
				}
				if tc.done && (!d.ModelCall.UsageAvailable || d.ModelCall.OutputTokens != 20) {
					t.Fatal("usage lost", d.ModelCall)
				}
			}
		})
	}
}

func TestAnswerPrefixBuffersEscapesAndIgnoresOtherFields(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"kind":"final","answer_markdown":"中\ud83d`, "中"},
		{`{"kind":"final","answer_markdown":"中\ud83d\ude80`, "中🚀"},
		{"{\"kind\":\"final\",\"answer_markdown\":\"x\xe4\xb8", "x"},
		{`{"kind":"tool_batch","calls":[{"arguments":{"answer_markdown":"secret"}}]}`, ""},
		{`{"kind":"final","answer_markdown":"draft","answer_markdown":"other"}`, ""},
		{`{"kind":"final","answer_markdown":"draft","completion_feedback":"reject"}`, ""},
	} {
		if got := answerPrefix(tc.raw, true); got != tc.want {
			t.Fatalf("prefix: got %q want %q", got, tc.want)
		}
	}
}

func TestModelStreamRejectsBrokenFraming(t *testing.T) {
	for _, tc := range []struct{ name, raw, code string }{
		{"duplicate", `data: {"choices":[],"choices":[]}` + "\n\ndata: [DONE]\n\n", "model_response_invalid"},
		{"missing-index", `data: {"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n", "model_response_invalid"},
		{"other-choice", `data: {"choices":[{"index":1,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n", "model_response_invalid"},
		{"after-finish", `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" + `data: {"choices":[{"index":0,"delta":{"content":"late"}}]}` + "\n\ndata: [DONE]\n\n", "model_response_invalid"},
		{"oversized-line", "data: " + strings.Repeat("x", 1<<20), "model_response_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readModelStream(strings.NewReader(tc.raw), func(string, string) {}, nil); agentcontract.ModelErrorCode(err) != tc.code {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamOptInDoesNotChangeMemoryPayload(t *testing.T) {
	model := &HTTPJSONDecisionModel{Model: "test", StreamAnswers: true}
	payload, err := model.requestPayload([]byte(`{"text":"remember"}`), "Extract memories")
	if err != nil || payload["stream"] != nil || payload["stream_options"] != nil {
		t.Fatal("memory transport changed", payload, err)
	}
}
