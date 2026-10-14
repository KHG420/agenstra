package modelapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestInterruptedModelBodyCannotReplayBoundedRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    int64
		attempts int64
		code     string
	}{
		{name: "bounded", limit: 10000, attempts: 1, code: "model_outcome_unknown"},
		{name: "unbounded", attempts: 3, code: "model_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writer.Header().Set("Content-Length", "1024")
				writer.WriteHeader(http.StatusOK)
				if _, err := writer.Write([]byte("{")); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.MaxAttempts = 3
			model.RetryBaseDelay = time.Nanosecond
			model.MaxOutputTokens = 512
			decision, err := model.Decide(t.Context(), agentcontract.ContextPacket{Schema: "agenstra.context.v1", ModelTokensRemaining: tc.limit}, "Return one decision.")
			if agentcontract.ModelErrorCode(err) != tc.code || requests.Load() != tc.attempts || decision.ModelCall == nil || int64(decision.ModelCall.Attempts) != tc.attempts || decision.ModelCall.UsageAvailable {
				t.Fatalf("interrupted request replayed or settled without a receipt: error=%v requests=%d metrics=%+v", err, requests.Load(), decision.ModelCall)
			}
		})
	}
}
