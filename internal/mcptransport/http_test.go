package mcptransport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMCPHTTPReadsCompleteSSEEventsForTheCurrentRequest(t *testing.T) {
	response := `{"jsonrpc":"2.0","id":1,"result":{"capacity":2400}}`
	for _, tc := range []struct {
		name, stream string
		wantError    bool
	}{
		{"single-line", ": keepalive\nevent: message\nid: event-1\ndata: " + response + "\n\n", false},
		{"crlf", "data: " + response + "\r\n\r\n", false},
		{"cr", "data: " + response + "\r\r", false},
		{"bom", "\xef\xbb\xbfdata: " + response + "\n\n", false},
		{"multiple-data-lines", "data:{\"jsonrpc\":\"2.0\",\ndata: \"id\":1,\ndata: \"result\":{\"capacity\":2400}}\n\n", false},
		{"server-request-same-id", "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n\ndata: " + response + "\n\n", false},
		{"server-request-other-id", "data: {\"jsonrpc\":\"2.0\",\"id\":20,\"method\":\"ping\"}\n\ndata: " + response + "\n\n", false},
		{"notification", "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\ndata: " + response + "\n\n", false},
		{"empty-data", "data:\n\ndata\n\ndata: " + response + "\n\n", false},
		{"unrelated-response", "data: {\"jsonrpc\":\"2.0\",\"id\":20,\"result\":{\"capacity\":1}}\n\ndata: " + response + "\n\n", false},
		{"unrelated-error", "data: {\"jsonrpc\":\"2.0\",\"id\":20,\"error\":{\"code\":-32601}}\n\ndata: " + response + "\n\n", false},
		{"trailing-garbage", "data: " + response + " garbage\n\n", true},
		{"two-json-values", "data: " + response + " " + response + "\n\n", true},
		{"invalid-later-data", "data: " + response + "\ndata: garbage\n\n", true},
		{"undelimited-event", "data: " + response + "\n", true},
		{"missing-version", "data: {\"id\":1,\"result\":{\"capacity\":2400}}\n\n", true},
		{"wrong-version", "data: {\"jsonrpc\":\"1.0\",\"id\":1,\"result\":{\"capacity\":2400}}\n\n", true},
		{"result-and-null-error", "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"capacity\":2400},\"error\":null}\n\n", true},
		{"matching-error", "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32601}}\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				if _, callErr := w.Write([]byte(tc.stream)); callErr != nil {
					t.Error(callErr)
				}
			}))
			defer server.Close()
			transport := &HTTP{url: server.URL, client: server.Client()}
			result, err := transport.Request(t.Context(), "tools/call", map[string]any{})
			if tc.wantError {
				if err == nil {
					t.Fatalf("invalid event became a confirmed result: %v", result)
				}
			} else if err != nil || fmt.Sprint(result["capacity"]) != "2400" {
				t.Fatalf("current response lost: result=%v err=%v", result, err)
			}
		})
	}
}

func TestMCPHTTPJSONResponseRequiresOneCompleteBoundedValue(t *testing.T) {
	response := `{"jsonrpc":"2.0","id":1,"result":{"capacity":2400}}`
	for _, tc := range []struct {
		name, payload string
		wantError     bool
	}{
		{"valid", response + "\n \t", false},
		{"missing-version", `{"id":1,"result":{"capacity":2400}}`, true},
		{"wrong-version", `{"jsonrpc":"1.0","id":1,"result":{"capacity":2400}}`, true},
		{"result-and-null-error", `{"jsonrpc":"2.0","id":1,"result":{"capacity":2400},"error":null}`, true},
		{"request-with-result", `{"jsonrpc":"2.0","id":1,"method":"ping","result":{"capacity":2400}}`, true},
		{"second-value", response + " " + response, true},
		{"trailing-garbage", response + " incomplete", true},
		{"oversized", response + strings.Repeat(" ", (16<<20)+1-len(response)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if _, callErr2 := w.Write([]byte(tc.payload)); callErr2 != nil {
					t.Error(callErr2)
				}
			}))
			defer server.Close()
			transport := &HTTP{url: server.URL, client: server.Client()}
			result, err := transport.Request(t.Context(), "tools/call", map[string]any{})
			if tc.wantError {
				if err == nil {
					t.Fatalf("invalid body became a confirmed result: %v", result)
				}
			} else if err != nil || fmt.Sprint(result["capacity"]) != "2400" {
				t.Fatalf("valid response lost: %v %v", result, err)
			}
		})
	}
}

func TestMCPHTTPReturnsOnCompleteEventWithoutWaitingForStreamClosure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if _, callErr3 := fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"capacity\":2400}}\n\n"); callErr3 != nil {
			t.Error(callErr3)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	transport := &HTTP{url: server.URL, client: server.Client()}
	result, err := transport.Request(ctx, "tools/call", map[string]any{})
	if err != nil || fmt.Sprint(result["capacity"]) != "2400" || ctx.Err() != nil {
		t.Fatalf("complete event waited for stream closure: %v %v", result, err)
	}
}

func TestMCPHTTPRecognizesSSEMediaTypes(t *testing.T) {
	for _, contentType := range []string{"text/event-stream", "Text/Event-Stream", "TEXT/EVENT-STREAM; charset=UTF-8", "text/event-stream ; charset=\"utf-8\""} {
		t.Run(contentType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				if _, callErr4 := fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"capacity\":2400}}\n\n"); callErr4 != nil {
					t.Error(callErr4)
				}
			}))
			defer server.Close()
			transport := &HTTP{url: server.URL, client: server.Client()}
			result, err := transport.Request(t.Context(), "tools/call", map[string]any{})
			if err != nil || fmt.Sprint(result["capacity"]) != "2400" {
				t.Fatalf("valid SSE content type was not recognized: %v %v", result, err)
			}
		})
	}
}
