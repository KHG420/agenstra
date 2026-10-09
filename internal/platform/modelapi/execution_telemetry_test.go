package modelapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestModelRetryProgressAndObserverFailure(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests%2 == 1 {
			w.WriteHeader(503)
			return
		}
		if callErr2 := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": `{"kind":"final","answer_markdown":"hello","fact_ids":[]}`}}}}); callErr2 != nil {
			t.Error(callErr2)
		}
	}))
	defer server.Close()
	m, callErr3 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr3 != nil {
		t.Error(callErr3)
	}
	m.RetryBaseDelay = time.Nanosecond
	var progress []agentcontract.ModelRequestProgress
	ctx := agentcontract.WithModelRequestObserver(t.Context(), func(p agentcontract.ModelRequestProgress) error { progress = append(progress, p); return nil })
	if _, err := m.Decide(ctx, agentcontract.ContextPacket{}, "hello"); err != nil {
		t.Fatal(err)
	}
	if len(progress) != 2 || progress[0].Kind != "model_retry_wait" || progress[0].RetryAt == 0 || progress[1].Attempt != 2 {
		t.Fatal(progress)
	}
	ctx = agentcontract.WithModelRequestObserver(t.Context(), func(agentcontract.ModelRequestProgress) error { return errors.New("checkpoint failed") })
	if _, err := m.Decide(ctx, agentcontract.ContextPacket{}, "hello"); err == nil || requests != 3 {
		t.Fatal(err, requests)
	}
}
