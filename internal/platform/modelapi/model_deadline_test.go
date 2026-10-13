package modelapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestModelRequestDeadlineRetainsCauseAndSafeTimeoutMetrics(t *testing.T) {
	for _, memory := range []bool{false, true} {
		name := "decision"
		if memory {
			name = "memory"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int64
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				<-release
			}))
			defer func() {
				close(release)
				server.Close()
			}()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", 5*time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var metrics *agentcontract.ModelCallMetrics
			if memory {
				var measured agentcontract.ModelCallMetrics
				_, measured, err = model.ExtractMemoriesMeasured(ctx, agentcontract.MemoryExtractionRequest{Text: "Use Chinese."})
				metrics = &measured
			} else {
				var decision agentcontract.Decision
				decision, err = model.Decide(ctx, agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "Return one decision.")
				metrics = decision.ModelCall
			}
			if !errors.Is(err, context.DeadlineExceeded) || metrics == nil || metrics.ErrorCode == nil || *metrics.ErrorCode != "model_timeout" {
				t.Fatalf("deadline cause or safe metrics lost: error=%v metrics=%+v", err, metrics)
			}
			if requests.Load() != 1 || metrics.Attempts != 1 || metrics.FormatError != "" || metrics.FormatRecovery {
				t.Fatal("deadline retried or became a format error", requests.Load(), metrics)
			}
		})
	}
}
