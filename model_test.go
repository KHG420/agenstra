package agenstra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
		_ = json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": content}}}})
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

func TestModelTransientRetriesPreserveRequest(t *testing.T) {
	requests := 0
	var first string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
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
		_ = json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": `{"kind":"final","answer_markdown":"ok","fact_ids":[]}`}}}})
	}))
	defer server.Close()
	m, _ := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
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
			m, _ := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
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
	m, _ := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
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
	m, _ := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	_, err := m.Decide(t.Context(), ContextPacket{}, "system")
	server.Close()
	if ErrorCode(err) != "model_rate_limited" || requests != 1 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"finish_reason": "length", "message": JSON{"content": `{"kind":"final","answer_markdown":"partial"}`}}}})
	}))
	defer server.Close()
	m, _ = NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	_, err = m.Decide(t.Context(), ContextPacket{}, "system")
	if ErrorCode(err) != "model_output_truncated" {
		t.Fatal(err)
	}
}
