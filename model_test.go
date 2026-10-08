package agenstra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPJSONDecisionModelStrictBoundary(t *testing.T) {
	status := 200
	content := `{"schema":"agenstra.decision.v1","kind":"final","answer_markdown":"Done","fact_ids":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("request boundary: %s", r.URL)
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		var request JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["response_format"].(map[string]any)["type"] != "json_object" {
			t.Error("missing JSON format")
		}
		if callErr := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": content}}}}); callErr != nil {
			t.Error(callErr)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("model", server.URL, "key", 0, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	decision, err := model.Decide(context.Background(), ContextPacket{Schema: "agenstra.context.v1"}, "system")
	if err != nil || decision.Kind != "final" {
		t.Fatalf("valid decision: %+v %v", decision, err)
	}
	content = "```json\n" + content + "\n```"
	decision, err = model.Decide(context.Background(), ContextPacket{}, "system")
	if err != nil || decision.AnswerMarkdown != "Done" {
		t.Fatalf("complete Markdown-framed decision: %+v %v", decision, err)
	}
	content = `{"schema":"agenstra.decision.v1","kind":"final","answer_markdown":"Done","unexpected":1}`
	_, err = model.Decide(context.Background(), ContextPacket{}, "system")
	if ErrorCode(err) != "model_decision_invalid" {
		t.Fatalf("extra field accepted: %v", err)
	}
	status = 503
	model.MaxAttempts = 1
	_, err = model.Decide(context.Background(), ContextPacket{}, "system")
	if ErrorCode(err) != "model_unavailable" {
		t.Fatalf("HTTP status: %v", err)
	}
}

func TestHTTPJSONDecisionModelOutputFraming(t *testing.T) {
	// This is the nested input shape that was lost when a fenced response forced
	// an unnecessary second model request during announcement publication.
	tool := `{"kind":"tool_batch","calls":[{"call_ref":"publish","capability":"notice.create","reason":"Publish the requested notice","arguments":{"operation":"create","arguments":{"request":{"title":"国庆特惠","content":"模型半价","targeting":{}}}}}]}`
	fenced := "```json\n" + tool + "\n```"
	for _, tc := range []struct {
		name, content, formatError string
	}{
		{"raw", tool, ""},
		{"json fence", fenced, ""},
		{"unlabelled fence", "```\n" + tool + "\n```", ""},
		{"CRLF and whitespace", " \r\n```JSON\r\n" + tool + "\r\n```\r\n\t", ""},
		{"prose before", "Here is the decision:\n" + fenced, "model_output_invalid_json"},
		{"prose after", fenced + "\nDone.", "model_output_invalid_json"},
		{"multiple fences", fenced + "\n" + fenced, "model_output_invalid_json"},
		{"multiple values", "```json\n" + tool + "\n{}\n```", "model_output_invalid_json"},
		{"unclosed fence", "```json\n" + tool, "model_output_invalid_json"},
		{"wrong label", "```javascript\n" + tool + "\n```", "model_output_invalid_json"},
		{"truncated", "```json\n{\"kind\":\"tool_batch\",\"calls\":[\n```", "model_output_invalid_json"},
		{"array", "```json\n[]\n```", "model_decision_schema_invalid"},
		{"unknown field", "```json\n{\"kind\":\"final\",\"answer_markdown\":\"ok\",\"unexpected\":true}\n```", "model_decision_schema_invalid"},
		{"invalid kind", "```json\n{\"kind\":\"run\"}\n```", "model_decision_schema_invalid"},
		{"tools alias", `{"schema":"agenstra.decision.v1","kind":"tool_batch","tools":[{"name":"notice.create","input":{}}]}`, "model_decision_schema_invalid"},
		{"decision alias", `{"decision":"tool_batch","calls":[{"name":"notice.create","arguments":{}}]}`, "model_decision_schema_invalid"},
		{"call name alias", `{"kind":"tool_batch","calls":[{"call_ref":"publish","name":"notice.create","arguments":{},"reason":"Publish the notice"}]}`, "model_decision_schema_invalid"},
		{"duplicate kind", `{"kind":"final","kind":"tool_batch","calls":[]}`, "model_output_duplicate_key"},
		{"escaped duplicate", `{"kind":"final","k\u0069nd":"tool_batch","calls":[]}`, "model_output_duplicate_key"},
		{"nested duplicate", "```json\n" + `{"kind":"tool_batch","calls":[{"call_ref":"publish","capability":"notice.create","arguments":{"operation":"create","arguments":{"request":{"status":"draft","status":"active"}}}}]}` + "\n```", "model_output_duplicate_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				memoryModelReply(t, w, tc.content)
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			decision, err := model.Decide(t.Context(), ContextPacket{}, "system")
			if requests.Load() != 1 || decision.ModelCall.Attempts != 1 || decision.ModelCall.FormatRecovery {
				t.Fatalf("local parsing changed request count: %d %+v", requests.Load(), decision.ModelCall)
			}
			if tc.formatError != "" {
				if ErrorCode(err) != "model_decision_invalid" || decision.ModelCall.FormatError != tc.formatError || len(decision.Calls) != 0 {
					t.Fatalf("invalid output reached decision: %+v %v", decision, err)
				}
				return
			}
			if err != nil || decision.Kind != "tool_batch" || len(decision.Calls) != 1 {
				t.Fatalf("complete output rejected: %+v %v", decision, err)
			}
			want := JSON{"operation": "create", "arguments": JSON{"request": JSON{"title": "国庆特惠", "content": "模型半价", "targeting": JSON{}}}}
			gotJSON, err := CanonicalJSON(decision.Calls[0].Arguments)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := CanonicalJSON(want)
			if err != nil || string(gotJSON) != string(wantJSON) {
				t.Fatalf("input wrapper changed: %s want %s (%v)", gotJSON, wantJSON, err)
			}
		})
	}
}

func TestRuntimeJSONRecoveryIsBoundedBeforeExecution(t *testing.T) {
	valid := `{"kind":"tool_batch","calls":[{"call_ref":"publish","capability":"notice.create","arguments":{},"reason":"Publish the notice"}]}`
	for _, tc := range []struct {
		name, first, second, correction string
		wantPending                     bool
	}{
		{"non JSON", "Here is the result: " + valid, "still not JSON", "no comments, trailing text, or multiple values", false},
		{"duplicate keys", `{"kind":"final","kind":"tool_batch","calls":[]}`, `{"kind":"final","kind":"tool_batch","calls":[]}`, "Each object key must occur only once", false},
		{"corrected output", "invalid JSON", "```json\n" + valid + "\n```", "no comments, trailing text, or multiple values", true},
		{"corrected tools alias", `{"schema":"agenstra.decision.v1","kind":"tool_batch","tools":[{"name":"notice.create","input":{}}]}`, valid, "Decision object field names (these are literal JSON keys):", true},
		{"corrected decision alias", `{"decision":"tool_batch","calls":[{"name":"notice.create","arguments":{}}]}`, valid, "Decision object field names (these are literal JSON keys):", true},
		{"repeated tools alias", `{"kind":"tool_batch","tools":[{"name":"notice.create","input":{}}]}`, `{"kind":"tool_batch","tools":[{"name":"notice.create","input":{}}]}`, "Decision object field names (these are literal JSON keys):", false},
		{"corrected uppercase call ref", strings.Replace(valid, `"publish"`, `"groups-getAll-1"`, 1), valid, "call_ref must match ^[a-z][a-z0-9-]{0,63}$", true},
		{"repeated uppercase call ref", strings.Replace(valid, `"publish"`, `"groups-getAll-1"`, 1), strings.Replace(valid, `"publish"`, `"groups-getAll-1"`, 1), "call_ref must match ^[a-z][a-z0-9-]{0,63}$", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if n == 1 {
					if strings.Contains(tc.name, "uppercase call ref") {
						var input struct{ Messages []struct{ Content string } }
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) != 2 || !strings.Contains(input.Messages[0].Content, tc.correction) {
							t.Error("first model request omitted the call reference constraint", err)
						}
					}
					memoryModelReply(t, w, tc.first)
					return
				}
				var input struct{ Messages []struct{ Content string } }
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) != 2 {
					t.Error("retry did not explain the rejection", err, input)
				} else {
					feedback := input.Messages[0].Content
					start := strings.LastIndex(feedback, "Your previous response")
					if start < 0 || !strings.Contains(feedback[start:], tc.correction) {
						t.Error("retry feedback did not explain the rejected decision shape")
					}
				}
				memoryModelReply(t, w, tc.second)
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			provider := &coreTestProvider{caps: map[string]CapabilityDescription{"notice.create": {Name: "notice.create", Effect: "write", ApprovalRequired: true}}}
			runtime := &AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"notice.create": true}, MaxModelRounds: 4}
			state, err := runtime.NewState("Publish a notice", "")
			if err != nil {
				t.Fatal(err)
			}
			if err = runtime.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 || state.RoundsUsed != 2 || len(state.ModelCalls) != 2 || !state.ModelCalls[1].FormatRecovery || provider.called != 0 {
				t.Fatalf("recovery budget or execution boundary: requests=%d state=%+v provider calls=%d", requests.Load(), state, provider.called)
			}
			if tc.wantPending {
				if len(state.Pending) != 1 || state.Pending[0].Call.Capability != "notice.create" {
					t.Fatal("corrected decision was not prepared", state.Pending)
				}
			} else if state.Status != "failed" || len(state.Pending) != 0 || state.ErrorCode == nil || *state.ErrorCode != "model_decision_invalid" {
				t.Fatal("invalid output was not stopped before preparing a call", state)
			}
		})
	}
}

func TestModelTransientRetriesPreserveRequest(t *testing.T) {
	requests := 0
	var first string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, callErr2 := io.ReadAll(r.Body)
		if callErr2 != nil {
			t.Error(callErr2)
		}
		if requests == 1 {
			first = string(body)
		} else if string(body) != first {
			t.Error("retry changed request")
		}
		if requests == 1 {
			w.WriteHeader(429)
			return
		}
		if requests == 2 {
			w.WriteHeader(503)
			return
		}
		if callErr3 := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": `{"kind":"final","answer_markdown":"ok","fact_ids":[]}`}}}}); callErr3 != nil {
			t.Error(callErr3)
		}
	}))
	defer server.Close()
	m, callErr4 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr4 != nil {
		t.Error(callErr4)
	}
	m.RetryBaseDelay = time.Nanosecond
	d, err := m.Decide(t.Context(), ContextPacket{}, "system")
	if err != nil || d.Kind != "final" || requests != 3 {
		t.Fatalf("decision=%+v err=%v requests=%d", d, err, requests)
	}
}

func TestModelRetryClassificationAndBounds(t *testing.T) {
	for _, tc := range []struct {
		status, requests int
		code             string
	}{
		{401, 1, "model_authentication_failed"}, {403, 1, "model_access_denied"},
		{400, 1, "model_http_error"}, {429, 3, "model_rate_limited"}, {503, 3, "model_unavailable"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(tc.status) }))
			defer server.Close()
			m, callErr5 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
			if callErr5 != nil {
				t.Error(callErr5)
			}
			m.RetryBaseDelay = time.Nanosecond
			_, err := m.Decide(t.Context(), ContextPacket{}, "system")
			if ErrorCode(err) != tc.code || requests != tc.requests {
				t.Fatalf("err=%v requests=%d", err, requests)
			}
		})
	}
}

func TestModelCancellationInterruptsRetryWait(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "4")
		w.WriteHeader(429)
		close(entered)
	}))
	defer server.Close()
	m, callErr6 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr6 != nil {
		t.Error(callErr6)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := m.Decide(ctx, ContextPacket{}, "system"); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry wait ignored cancellation")
	}
}

func TestModelServerRetryAfterBoundAndTruncatedOutput(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	}))
	m, callErr7 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr7 != nil {
		t.Error(callErr7)
	}
	_, err := m.Decide(t.Context(), ContextPacket{}, "system")
	server.Close()
	if ErrorCode(err) != "model_rate_limited" || requests != 1 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if callErr8 := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"finish_reason": "length", "message": JSON{"content": `{"kind":"final","answer_markdown":"partial"}`}}}}); callErr8 != nil {
			t.Error(callErr8)
		}
	}))
	defer server.Close()
	var callErr9 error
	m, callErr9 = NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr9 != nil {
		t.Error(callErr9)
	}
	_, err = m.Decide(t.Context(), ContextPacket{}, "system")
	if ErrorCode(err) != "model_output_truncated" {
		t.Fatal(err)
	}
}
