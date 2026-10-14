package modelapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

type retryObserverTransport struct {
	calls               int
	respectCancellation bool
}

func (r *retryObserverTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r.calls++
	if r.respectCancellation {
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
	}
	status, body := 503, `{"error":{"code":"temporary_unavailable"}}`
	if r.calls > 1 {
		status, body = 200, `{"choices":[{"message":{"content":"{\"kind\":\"final\",\"answer_markdown\":\"ok\",\"fact_ids\":[]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":20}}`
	}
	// Host-owned transports need not enforce cancellation. The adapter must not
	// begin another attempt when its observer has just cancelled the caller.
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestRetryObserverCancellationPreventsNextTransportAttempt(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "live caller", true: "cancelled observer"}[stop], func(t *testing.T) {
			transport := &retryObserverTransport{}
			client := &http.Client{Transport: transport}
			model, err := NewHTTPJSONDecisionModel("compatible-model", "http://local.invalid/v1", "test-key", time.Second, client)
			if err != nil {
				t.Fatal(err)
			}
			model.MaxAttempts = 2
			model.RetryBaseDelay = time.Nanosecond
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			retries := 0
			ctx = agentcontract.WithModelRequestObserver(ctx, func(progress agentcontract.ModelRequestProgress) error {
				if progress.Kind == "model_retry_started" {
					retries++
					if stop {
						cancel()
					}
				}
				return nil
			})
			decision, err := model.Decide(ctx, agentcontract.ContextPacket{}, "reply")
			want := 2
			if stop {
				want = 1
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled observer still allowed transport: calls=%d err=%v", transport.calls, err)
				}
			} else if err != nil || decision.Kind != "final" || decision.AnswerMarkdown != "ok" || !decision.ModelCall.UsageAvailable {
				t.Fatalf("normal retry changed: %+v %v", decision, err)
			}
			if transport.calls != want || decision.ModelCall == nil || decision.ModelCall.Attempts != want || retries != 1 || model.Client != client || model.Client.Transport != transport {
				t.Fatalf("cancelled observer counted or sent a new attempt, or changed caller client: calls=%d metrics=%+v retries=%d", transport.calls, decision.ModelCall, retries)
			}
		})
	}
}

func TestRetryObserverCancellationDoesNotMultiplyUnknownRuntimeBudget(t *testing.T) {
	transport := &retryObserverTransport{respectCancellation: true}
	model, err := NewHTTPJSONDecisionModel("compatible-model", "http://local.invalid/v1", "test-key", time.Second, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	model.MaxAttempts = 2
	model.RetryBaseDelay = time.Nanosecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = agentcontract.WithModelRequestObserver(ctx, func(progress agentcontract.ModelRequestProgress) error {
		if progress.Kind == "model_retry_started" {
			cancel()
		}
		return nil
	})
	runtime := &reactcore.AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model, MaxModelTokens: 200000, MaxModelOutputTokens: 64}
	result, err := runtime.Run(ctx, "reply")
	if !errors.Is(err, context.Canceled) || result.Status != "failed" || len(result.ModelCalls) != 1 || result.ModelCalls[0].Attempts != 1 || !result.ModelCalls[0].Reservation || result.ModelCalls[0].UsageAvailable || result.ModelUsage.Requests != 1 || result.ModelUsage.BudgetTokens != 200000 || transport.calls != 1 || len(result.Observations) != 0 {
		t.Fatalf("cancelled retry multiplied the unknown dispatched budget: transport=%d usage=%+v calls=%+v err=%v", transport.calls, result.ModelUsage, result.ModelCalls, err)
	}
}
