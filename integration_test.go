package agenstra

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPModelRESTDurableEndToEnd(t *testing.T) {
	var externalCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalCalls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/records/R-1" {
			t.Errorf("external request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, 200, map[string]any{"id": "R-1"})
	}))
	defer upstream.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-model-token" {
			t.Errorf("model request: %s", r.URL.Path)
		}
		var body struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		var packet ContextPacket
		for _, message := range body.Messages {
			if message.Role == "user" {
				if e := json.Unmarshal([]byte(message.Content), &packet); e != nil {
					t.Error(e)
				}
				// The first user message is the cumulative packet; later messages
				// can present the saved outcome of an already executed call.
				break
			}
		}
		decision := Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "read-1", Capability: "records.get", Arguments: JSON{"path": JSON{"record_id": "R-1"}}, Reason: "Read reviewed record"}}}
		if len(packet.Facts) > 0 {
			decision = Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Record R-1 verified", FactIDs: []string{packet.Facts[0].FactID}}
		}
		content, e := json.Marshal(decision)
		if e != nil {
			t.Error(e)
		}
		writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}})
	}))
	defer gateway.Close()
	path, e := filepath.Abs("testdata/rest-v2.json")
	if e != nil {
		t.Fatal(e)
	}
	dep := &Deployment{Config: DeploymentConfig{Packs: map[string]PackConfig{"records": {Path: path}}, Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY", Packs: map[string]ConnectionConfig{"records": {Environment: map[string]string{"RECORDS_URL": "UPSTREAM_URL"}, GrantedCapabilities: []string{"records.get"}, AllowModelData: true}}}}, Settings: DefaultHostSettings()}, Environment: map[string]string{"ALICE_KEY": "test-alice-key", "UPSTREAM_URL": upstream.URL}}
	store := testStore(t)
	model, e := NewHTTPJSONDecisionModel("test-model", gateway.URL, "test-model-token", time.Second, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(model.Close)
	host := NewAgentHost(store, dep.ProviderFactory, model, dep.PolicyResolver)
	server, e := NewHTTPServer(host, dep, true, 10*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(server.Close)
	api := httptest.NewServer(server.Handler())
	defer api.Close()
	request := func(method, path string, data any) (int, map[string]any) {
		t.Helper()
		var reader io.Reader
		if data != nil {
			b, e := json.Marshal(data)
			if e != nil {
				t.Fatal(e)
			}
			reader = bytes.NewReader(b)
		}
		req, e := http.NewRequest(method, api.URL+path, reader)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer test-alice-key")
		req.Header.Set("Content-Type", "application/json")
		response, e := api.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Error(err)
			}
		}()
		var result map[string]any
		if e = json.NewDecoder(response.Body).Decode(&result); e != nil {
			t.Fatal(e)
		}
		return response.StatusCode, result
	}
	code, r := request("POST", "/runs", map[string]any{"pack_id": "records", "instruction": "Look up record R-1", "request_id": "e2e-record-1"})
	if code != 200 {
		t.Fatalf("create: %d %v", code, r)
	}
	id := r["run_id"].(string)
	deadline := time.Now().Add(3 * time.Second)
	for r["status"] != "completed" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		code, r = request("GET", "/runs/"+id, nil)
		if code != 200 {
			t.Fatalf("get: %d %v", code, r)
		}
	}
	if r["status"] != "completed" {
		t.Fatalf("run: %v", r)
	}
	runtime := r["state"].(map[string]any)["runtime"].(map[string]any)
	if runtime["answer_markdown"] != "Record R-1 verified" || externalCalls.Load() != 1 {
		t.Fatalf("outcome: %v calls %d", runtime, externalCalls.Load())
	}
	factID := r["state"].(map[string]any)["artifact_ids"].([]any)[0].(string)
	code, fact := request("GET", "/runs/"+id+"/artifacts/"+factID, nil)
	if code != 200 || fact["source_capability"] != "records.get" {
		t.Fatalf("artifact: %d %v", code, fact)
	}
	code, again := request("POST", "/runs", map[string]any{"pack_id": "records", "instruction": "Look up record R-1", "request_id": "e2e-record-1"})
	if code != 200 || again["run_id"] != id || externalCalls.Load() != 1 {
		t.Fatalf("request replay: %d %v", code, again)
	}
}
