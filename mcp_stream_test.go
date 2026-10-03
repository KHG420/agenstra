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
				_, _ = w.Write([]byte(tc.stream))
			}))
			defer server.Close()
			transport := &httpMCP{url: server.URL, client: server.Client()}
			result, err := transport.Request(t.Context(), "tools/call", JSON{})
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
				_, _ = w.Write([]byte(tc.payload))
			}))
			defer server.Close()
			transport := &httpMCP{url: server.URL, client: server.Client()}
			result, err := transport.Request(t.Context(), "tools/call", JSON{})
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
		_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"capacity\":2400}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	transport := &httpMCP{url: server.URL, client: server.Client()}
	result, err := transport.Request(ctx, "tools/call", JSON{})
	if err != nil || fmt.Sprint(result["capacity"]) != "2400" || ctx.Err() != nil {
		t.Fatalf("complete event waited for stream closure: %v %v", result, err)
	}
}

func TestMCPHTTPRecognizesSSEMediaTypes(t *testing.T) {
	for _, contentType := range []string{"text/event-stream", "Text/Event-Stream", "TEXT/EVENT-STREAM; charset=UTF-8", "text/event-stream ; charset=\"utf-8\""} {
		t.Run(contentType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"capacity\":2400}}\n\n")
			}))
			defer server.Close()
			transport := &httpMCP{url: server.URL, client: server.Client()}
			result, err := transport.Request(t.Context(), "tools/call", JSON{})
			if err != nil || fmt.Sprint(result["capacity"]) != "2400" {
				t.Fatalf("valid SSE content type was not recognized: %v %v", result, err)
			}
		})
	}
}

func TestMCPPackConnectsAndInvokesUsingMultilineSSE(t *testing.T) {
	tool := JSON{"name": "record.read", "description": "Read record", "inputSchema": JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}, "outputSchema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request["method"] == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result JSON
		switch request["method"] {
		case "initialize":
			result = JSON{"protocolVersion": "2025-03-26", "capabilities": JSON{}, "serverInfo": JSON{"name": "test", "version": "1"}}
		case "tools/list":
			result = JSON{"tools": []any{tool}}
		case "tools/call":
			result = JSON{"content": []any{}, "structuredContent": JSON{"id": "R1"}}
		default:
			t.Errorf("unexpected method: %v", request["method"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		raw, err := json.MarshalIndent(JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			_, _ = fmt.Fprintf(w, "data: %s\n", line)
		}
		_, _ = fmt.Fprint(w, "\n")
	}))
	defer server.Close()
	manifest := JSON{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1", "guidance": "Read records", "source": JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{JSON{"name": tool["name"], "effect": "read", "contract_sha256": MCPContractDigest(tool)}}}
	pack, err := OpenMCPPack(t.Context(), writeTestManifest(t, manifest), map[string]string{"MCP_URL": server.URL})
	if err != nil {
		t.Fatalf("valid streaming server cannot be connected: %v", err)
	}
	defer pack.Close()
	result, err := pack.Invoke(t.Context(), "record.read", JSON{}, nil)
	if err != nil || result.ErrorCode != "" || result.Data["id"] != "R1" {
		t.Fatalf("streaming business result lost: %+v %v", result, err)
	}
}

func TestMCPInvalidWriteReceiptPausesWithoutReplaying(t *testing.T) {
	for _, responseCase := range []string{"incomplete-sse", "missing-version", "wrong-version", "result-and-null-error", "request-with-result"} {
		t.Run(responseCase, func(t *testing.T) { testMCPInvalidWriteReceipt(t, responseCase) })
	}
}

func testMCPInvalidWriteReceipt(t *testing.T, responseCase string) {
	t.Helper()
	tool := JSON{"name": "record.create", "description": "Create record", "inputSchema": JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}, "outputSchema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var result JSON
		switch request["method"] {
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "initialize":
			result = JSON{"protocolVersion": "2025-03-26", "capabilities": JSON{}, "serverInfo": JSON{"name": "test", "version": "1"}}
		case "tools/list":
			result = JSON{"tools": []any{tool}}
		case "tools/call":
			writes.Add(1)
			result = JSON{"content": []any{}, "structuredContent": JSON{"id": "R1"}}
			reply := JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}
			switch responseCase {
			case "incomplete-sse":
				w.Header().Set("Content-Type", "text/event-stream")
				raw, _ := json.Marshal(reply)
				_, _ = fmt.Fprintf(w, "data: %s\ndata: incomplete\n\n", raw)
				return
			case "missing-version":
				delete(reply, "jsonrpc")
			case "wrong-version":
				reply["jsonrpc"] = "1.0"
			case "result-and-null-error":
				reply["error"] = nil
			case "request-with-result":
				reply["method"] = "ping"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(reply)
			return
		default:
			t.Errorf("unexpected method: %v", request["method"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JSON{"jsonrpc": "2.0", "id": request["id"], "result": result})
	}))
	defer server.Close()
	manifest := JSON{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1", "guidance": "Create records", "source": JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{JSON{"name": tool["name"], "effect": "write", "contract_sha256": MCPContractDigest(tool)}}}
	pack, err := OpenMCPPack(t.Context(), writeTestManifest(t, manifest), map[string]string{"MCP_URL": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	model := &hostModel{decisions: []Decision{{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "create-1", Capability: "record.create", Arguments: JSON{}, Reason: "Create the requested record"}}}}}
	host := NewAgentHost(testStore(t), func(context.Context, string, string) (CapabilityProvider, error) { return pack, nil }, model, func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{GrantedCapabilities: map[string]bool{"record.create": true}, AllowModelData: true}, nil
	})
	run, err := host.Create(t.Context(), "alice", "records", "Create one record", "invalid-mcp-write")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		run, err = host.Drive(t.Context(), run.RunID, "alice")
		if err != nil || run.Status != "needs_reconciliation" {
			t.Fatalf("invalid receipt confirmed the write: status=%s err=%v", run.Status, err)
		}
		if writes.Load() != 1 {
			t.Fatalf("business operation replayed: %d", writes.Load())
		}
	}
}
