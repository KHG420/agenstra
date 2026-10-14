package modelapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestBoundedModelBudgetCannotReplayTimedOutRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    int64
		attempts int64
	}{
		{name: "bounded", limit: 10000, attempts: 1},
		{name: "unbounded", attempts: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests.Add(1)
				<-release
			}))
			defer func() { close(release); server.Close() }()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", 50*time.Millisecond, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.MaxAttempts = 3
			model.RetryBaseDelay = time.Nanosecond
			model.MaxOutputTokens = 512
			decision, err := model.Decide(t.Context(), agentcontract.ContextPacket{Schema: "agenstra.context.v1", ModelTokensRemaining: tc.limit}, "Return one decision.")
			if agentcontract.ModelErrorCode(err) != "model_timeout" || requests.Load() != tc.attempts || decision.ModelCall == nil || int64(decision.ModelCall.Attempts) != tc.attempts {
				t.Fatalf("bounded request replayed after unknown output: error=%v requests=%d metrics=%+v", err, requests.Load(), decision.ModelCall)
			}
		})
	}
}
