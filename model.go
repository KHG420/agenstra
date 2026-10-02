package agenstra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ModelDecisionError struct{ Kind string }

func (e ModelDecisionError) Error() string { return e.Kind }
func (e ModelDecisionError) Code() string  { return e.Kind }

type HTTPJSONDecisionModel struct {
	Model   string
	BaseURL string
	APIKey  string
	Timeout time.Duration
	Client  *http.Client
	// Zero values use bounded defaults. MaxAttempts includes the first request;
	// set it to one to disable transport retries.
	MaxAttempts    int
	RetryBaseDelay time.Duration
	MaxRetryDelay  time.Duration
}

func NewHTTPJSONDecisionModel(model, baseURL, apiKey string, timeout time.Duration, client *http.Client) (*HTTPJSONDecisionModel, error) {
	if model == "" || baseURL == "" || apiKey == "" {
		return nil, errors.New("model, base_url, and api_key are required")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &HTTPJSONDecisionModel{Model: model, BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Timeout: timeout, Client: client}, nil
}
func (m *HTTPJSONDecisionModel) Decide(ctx context.Context, packet ContextPacket, prompt string) (Decision, error) {
	contextJSON, err := CanonicalJSON(packet)
	if err != nil {
		return Decision{}, ModelDecisionError{"model_decision_invalid"}
	}
	body, err := m.requestJSON(ctx, contextJSON, prompt)
	if err != nil {
		return Decision{}, err
	}
	decision, err := strictDecision(body)
	if err != nil {
		var oversized DecisionTooManyCallsError
		if errors.As(err, &oversized) {
			return Decision{}, oversized
		}
		return Decision{}, ModelDecisionError{"model_decision_invalid"}
	}
	return decision, nil
}
func (m *HTTPJSONDecisionModel) requestJSON(ctx context.Context, input []byte, prompt string) ([]byte, error) {
	payload := JSON{"model": m.Model, "response_format": JSON{"type": "json_object"}, "messages": []any{JSON{"role": "system", "content": prompt}, JSON{"role": "user", "content": string(input)}}}
	raw, err := CanonicalJSON(payload)
	if err != nil {
		return nil, ModelDecisionError{"model_decision_invalid"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, ModelDecisionError{"model_unavailable"}
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := *m.Client
	if client.Timeout == 0 {
		client.Timeout = m.Timeout
	}
	attempts, delay, maxDelay := m.retrySettings()
	var body []byte
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request := req.Clone(ctx)
		request.Body = io.NopCloser(bytes.NewReader(raw))
		res, requestErr := client.Do(request)
		code, retry, retryAfter := "", false, time.Duration(0)
		if requestErr != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			code, retry = "model_unavailable", true
			var timeout net.Error
			if errors.As(requestErr, &timeout) && timeout.Timeout() {
				code = "model_timeout"
			}
		} else {
			code, retry = modelHTTPError(res.StatusCode)
			retryAfter = modelRetryAfter(res.Header.Get("Retry-After"), time.Now())
			if code == "" {
				body, requestErr = io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
				if requestErr != nil {
					code, retry = "model_unavailable", true
				} else if len(body) > 8<<20 {
					code = "model_response_too_large"
				}
			}
			_ = res.Body.Close()
		}
		if code == "" {
			break
		}
		wait := max(delay, retryAfter)
		// Do not retry earlier than the server requested or exceed our wait bound.
		if !retry || attempt+1 >= attempts || wait > maxDelay {
			return nil, ModelDecisionError{code}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, maxDelay)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Choices) == 0 {
		return nil, ModelDecisionError{"model_decision_invalid"}
	}
	if envelope.Choices[0].Message.Refusal != "" || envelope.Choices[0].FinishReason == "content_filter" {
		return nil, ModelDecisionError{"model_refused"}
	}
	if envelope.Choices[0].FinishReason == "length" {
		return nil, ModelDecisionError{"model_output_truncated"}
	}
	if envelope.Choices[0].Message.Content == "" {
		return nil, ModelDecisionError{"model_decision_invalid"}
	}
	return []byte(envelope.Choices[0].Message.Content), nil
}

func (m *HTTPJSONDecisionModel) retrySettings() (int, time.Duration, time.Duration) {
	attempts, delay, maximum := m.MaxAttempts, m.RetryBaseDelay, m.MaxRetryDelay
	if attempts <= 0 {
		attempts = 3
	}
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}
	if maximum <= 0 {
		maximum = 5 * time.Second
	}
	return min(attempts, 10), min(delay, maximum), maximum
}

func modelHTTPError(status int) (string, bool) {
	switch {
	case status < 300:
		return "", false
	case status == 401:
		return "model_authentication_failed", false
	case status == 403:
		return "model_access_denied", false
	case status == 429:
		return "model_rate_limited", true
	case status == 408 || status == 500 || status == 502 || status == 503 || status == 504:
		return "model_unavailable", true
	default:
		return "model_http_error", false
	}
}

func modelRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, date.Sub(now))
	}
	return 0
}
func (m *HTTPJSONDecisionModel) Close() error { return nil }
