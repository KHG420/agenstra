package agenstra_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

func TestSDKCancellationBeforeModelHeadersKeepsUnknownBudget(t *testing.T) {
	received := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			return
		}
		close(received)
		// Cancellation after the entire request reaches the provider is not a usage receipt.
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-received:
			cancel()
		case <-ctx.Done():
		}
	}()
	model, err := agenstra.NewHTTPJSONDecisionModel("compatible-test-model", server.URL, "test-key", 30*time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &agenstra.AgentRuntime{Provider: &recordProvider{}, Model: model, MaxModelTokens: 200000, MaxModelOutputTokens: 64}
	result, err := runtime.Run(ctx, "Hello; respond briefly without invoking business capabilities.")
	<-stopped
	if !errors.Is(err, context.Canceled) || result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != "model_unavailable" {
		t.Fatalf("caller cancellation was not propagated: status=%s code=%v err=%v", result.Status, result.ErrorCode, err)
	}
	if requests.Load() != 1 || len(result.ModelCalls) != 1 || result.ModelCalls[0].Attempts != 1 || !result.ModelCalls[0].Reservation || result.ModelUsage.BudgetTokens != 200000 || result.ModelUsage.InputTokens != 0 || result.ModelUsage.OutputTokens != 0 {
		t.Fatalf("dispatched cancellation incorrectly settled unknown output: requests=%d calls=%+v usage=%+v", requests.Load(), result.ModelCalls, result.ModelUsage)
	}
}
