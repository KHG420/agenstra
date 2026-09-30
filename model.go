package agenstra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	payload := JSON{"model": m.Model, "response_format": JSON{"type": "json_object"}, "messages": []any{JSON{"role": "system", "content": prompt}, JSON{"role": "user", "content": string(contextJSON)}}}
	raw, err := CanonicalJSON(payload)
	if err != nil {
		return Decision{}, ModelDecisionError{"model_decision_invalid"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return Decision{}, ModelDecisionError{"model_unavailable"}
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := *m.Client
	if client.Timeout == 0 {
		client.Timeout = m.Timeout
	}
	res, err := client.Do(req)
	if err != nil {
		return Decision{}, ModelDecisionError{"model_unavailable"}
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return Decision{}, ModelDecisionError{"model_http_error"}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return Decision{}, ModelDecisionError{"model_decision_invalid"}
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Choices) == 0 || envelope.Choices[0].Message.Content == "" {
		return Decision{}, ModelDecisionError{"model_decision_invalid"}
	}
	decision, err := strictDecision([]byte(envelope.Choices[0].Message.Content))
	if err != nil {
		var oversized DecisionTooManyCallsError
		if errors.As(err, &oversized) {
			return Decision{}, oversized
		}
		return Decision{}, ModelDecisionError{"model_decision_invalid"}
	}
	return decision, nil
}
func (m *HTTPJSONDecisionModel) Close() error { return nil }
