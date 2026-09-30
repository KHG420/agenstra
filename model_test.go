package agenstra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
	_, err = model.Decide(context.Background(), ContextPacket{}, "system")
	if ErrorCode(err) != "model_http_error" {
		t.Fatalf("HTTP status: %v", err)
	}
}
