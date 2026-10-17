package host

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func TestRESTMalformedWriteResponsePausesWithoutReplayingBusinessOperation(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		writes.Add(1)
		if _, callErr3 := w.Write([]byte(`{"id":"R1"} {"id":"R2"}`)); callErr3 != nil {
			t.Error(callErr3)
		}
	}))
	defer server.Close()
	manifest := agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Create records", "base_url_env": "API_URL", "capabilities": []any{agentcontract.JSON{
		"name": "record.create", "description": "Create record", "method": "POST", "path": "/records", "effect": "write",
		"input_schema":  agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
		"output_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false},
	}}}
	pack, err := capabilitypack.LoadRestPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(pack.Close)
	model := &hostModel{decisions: []agentcontract.Decision{{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "create-1", Capability: "record.create", Arguments: agentcontract.JSON{}, Reason: "Create the requested record"}}}}}
	host := NewAgentHost(testStore(t), func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return pack, nil }, model, func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{"record.create": true}, AllowModelData: true}, nil
	})
	run, err := host.Create(t.Context(), "alice", "records", "Create one record", "malformed-write")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		run, err = host.Drive(t.Context(), run.RunID, "alice")
		if err != nil || run.Status != "needs_reconciliation" {
			t.Fatalf("malformed write result was treated as confirmed: %+v %v", run, err)
		}
		if writes.Load() != 1 {
			t.Fatalf("business operation replayed: %d", writes.Load())
		}
	}
}

func TestRESTPostReadRecoversLostResponseWithoutReplayingCompletedRead(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/query" {
			t.Errorf("unexpected query request: %s %s", r.Method, r.URL.Path)
		}
		if reads.Add(1) == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test server cannot simulate response loss")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
			return
		}
		if _, err := w.Write([]byte(`{"id":"R1"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	defer server.Client().CloseIdleConnections()
	manifest := agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Query records", "base_url_env": "API_URL", "capabilities": []any{agentcontract.JSON{
		"name": "record.query", "description": "Query existing record", "method": "POST", "path": "/query", "effect": "read",
		"input_schema":  agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
		"output_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false},
	}}}
	pack, err := capabilitypack.LoadRestPack(writeTestManifest(t, manifest), map[string]string{"API_URL": server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model := &hostModel{decisions: []agentcontract.Decision{{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "query-1", Capability: "record.query", Arguments: agentcontract.JSON{}, Reason: "Query the requested record"}}}}}
	host := NewAgentHost(testStore(t), func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return pack, nil }, model, func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{"record.query": true}, AllowModelData: true}, nil
	})
	now := runstore.UnixNow()
	host.Clock = func() float64 { return now }
	run, err := host.Create(t.Context(), "alice", "records", "Query one existing record", "post-read-response-loss")
	if err != nil {
		t.Fatal(err)
	}
	run, err = host.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" || reads.Load() != 1 {
		t.Fatalf("lost read response should wait for recovery: status=%s requests=%d err=%v", run.Status, reads.Load(), err)
	}
	now += 10
	run, err = host.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || reads.Load() != 2 {
		t.Fatalf("read was not recovered: status=%s requests=%d err=%v", run.Status, reads.Load(), err)
	}
	run, err = host.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || reads.Load() != 2 {
		t.Fatalf("completed read was replayed: status=%s requests=%d err=%v", run.Status, reads.Load(), err)
	}
}
