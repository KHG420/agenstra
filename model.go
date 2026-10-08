package agenstra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ModelDecisionError carries a safe model transport or format error code.
type ModelDecisionError struct{ Kind string }

// Error returns the safe error identifier.
func (e ModelDecisionError) Error() string { return e.Kind }

// Code exposes the stable identifier used by framework error handling.
func (e ModelDecisionError) Code() string { return e.Kind }

// modelErrorCode exposes deliberate codes while keeping uncoded adapter errors private.
func modelErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	var host *HostError
	if errors.As(err, &host) {
		return host.Code
	}
	var deployment *DeploymentError
	if errors.As(err, &deployment) {
		return deployment.Code
	}
	var registry *RegistryError
	if errors.As(err, &registry) {
		return registry.Code
	}
	// Existing decision and projection validators return these fixed safe strings.
	// Preserve their retry/budget behavior without exposing arbitrary error text.
	switch code := err.Error(); code {
	case "model_decision_invalid", "model_context_measurement_failed", "model_output_reserve_required", "context_too_large":
		return code
	}
	return "model_unavailable"
}

// HTTPJSONDecisionModel adapts a compatible JSON chat endpoint to typed decisions.
// Configure it before concurrent use; supplied HTTP clients remain caller-owned.
type HTTPJSONDecisionModel struct {
	Profile                    string
	APIType                    string
	Thinking                   string
	ReasoningEffort            string
	Temperature                *float64
	CachedInputPricePerMillion *float64
	PricesConfigured           bool
	ContextWindowTokens        int64
	MaxInputTokens             int64
	ProtocolReserveTokens      int64
	CountInputTokens           func(model string, payload []byte) (int64, error)
	Model                      string
	BaseURL                    string
	APIKey                     string
	Timeout                    time.Duration
	Client                     *http.Client
	// Zero values use bounded defaults. MaxAttempts includes the first request;
	// set it to one to disable transport retries.
	MaxAttempts     int
	RetryBaseDelay  time.Duration
	MaxRetryDelay   time.Duration
	MaxOutputTokens int
	// TokenLimitField defaults to max_tokens; set max_completion_tokens when
	// required by the selected compatible gateway.
	TokenLimitField       string
	InputPricePerMillion  float64
	OutputPricePerMillion float64
}

// NewHTTPJSONDecisionModel configures a bounded model adapter without contacting the service.
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

// Decide requests and validates one typed decision, preserving request usage on failure.
func (m *HTTPJSONDecisionModel) Decide(ctx context.Context, packet ContextPacket, prompt string) (decision Decision, resultErr error) {
	metrics := &ModelCallMetrics{}
	started := time.Now()
	ctx = context.WithValue(ctx, modelMetricsKey{}, metrics)
	ctx = context.WithValue(ctx, modelTokenBudgetKey{}, packet.ModelTokensRemaining)
	defer func() {
		metrics.ElapsedMilliseconds = time.Since(started).Milliseconds()
		if resultErr != nil {
			metrics.ErrorCode = strptr(ErrorCode(resultErr))
		}
		decision.ModelCall = metrics
	}()
	contextJSON, err := CanonicalJSON(packet)
	if err != nil {
		return Decision{}, ModelDecisionError{"model_decision_invalid"}
	}
	requestModel := *m
	if packet.MaxModelInputTokens > 0 && (requestModel.MaxInputTokens == 0 || packet.MaxModelInputTokens < requestModel.MaxInputTokens) {
		requestModel.MaxInputTokens = packet.MaxModelInputTokens
	}
	if packet.MaxModelOutputTokens > 0 && (requestModel.MaxOutputTokens <= 0 || packet.MaxModelOutputTokens < requestModel.MaxOutputTokens) {
		requestModel.MaxOutputTokens = packet.MaxModelOutputTokens
	}
	body, err := requestModel.requestJSON(ctx, contextJSON, prompt)
	if err != nil {
		return Decision{}, err
	}
	if detail := modelJSONFormatError(body); detail != "" {
		metrics.FormatError = detail
		return Decision{}, ModelDecisionError{"model_decision_invalid"}
	}
	decision, err = strictDecision(body)
	if err != nil {
		metrics.FormatError = modelContentError(body)
		var oversized DecisionTooManyCallsError
		if errors.As(err, &oversized) {
			return Decision{}, oversized
		}
		return Decision{}, ModelDecisionError{"model_decision_invalid"}
	}
	return decision, nil
}
func (m *HTTPJSONDecisionModel) requestJSON(ctx context.Context, input []byte, prompt string) ([]byte, error) {
	if metrics := modelMetrics(ctx); metrics != nil {
		metrics.Profile, metrics.Model, metrics.APIType = m.Profile, m.Model, m.APIType
		metrics.Thinking, metrics.ReasoningEffort = m.Thinking, m.ReasoningEffort
	}
	payload, err := m.requestPayload(input, prompt)
	if err != nil {
		return nil, err
	}
	raw, err := CanonicalJSON(payload)
	if err != nil {
		return nil, ModelDecisionError{"model_decision_invalid"}
	}
	outputLimit := int64(m.MaxOutputTokens)
	if metrics := modelMetrics(ctx); metrics != nil {
		// UTF-8 bytes plus a framing allowance are a bounded fallback, not a
		// tokenizer result. Provider usage replaces these estimates when available.
		measurement, measureErr := m.measurePayload(raw)
		if measureErr != nil {
			return nil, measureErr
		}
		metrics.EstimatedInputTokens = measurement.Tokens
		if remaining := packetTokenBudget(ctx); remaining > 0 && metrics.EstimatedInputTokens >= remaining {
			return nil, ModelDecisionError{"model_token_budget_exhausted"}
		}
		if remaining := packetTokenBudget(ctx); remaining > 0 {
			limit := remaining - metrics.EstimatedInputTokens
			if m.MaxOutputTokens > 0 {
				limit = min(limit, int64(m.MaxOutputTokens))
			}
			field := m.TokenLimitField
			if field == "" {
				field = "max_tokens"
			}
			if field != "max_tokens" && field != "max_completion_tokens" {
				return nil, ModelDecisionError{"model_token_limit_invalid"}
			}
			payload[field] = limit
			outputLimit = limit
			raw, err = CanonicalJSON(payload)
			if err != nil {
				return nil, ModelDecisionError{"model_decision_invalid"}
			}
		}
	}
	if m.MaxInputTokens > 0 || m.ContextWindowTokens > 0 {
		measured, measureErr := m.measurePayload(raw)
		if measureErr != nil {
			return nil, measureErr
		}
		limit := m.MaxInputTokens
		if m.ContextWindowTokens > 0 {
			if m.MaxOutputTokens <= 0 {
				return nil, ModelDecisionError{"model_output_reserve_required"}
			}
			available := m.ContextWindowTokens - int64(m.MaxOutputTokens) - m.ProtocolReserveTokens
			if available <= 0 {
				return nil, ModelDecisionError{"context_too_large"}
			}
			if limit == 0 || available < limit {
				limit = available
			}
		}
		if measured.Tokens > limit {
			return nil, ModelDecisionError{"context_too_large"}
		}
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
		if attempt > 0 {
			if err := observeModelRequest(ctx, ModelRequestProgress{Kind: "model_retry_started", Attempt: attempt + 1}); err != nil {
				return nil, err
			}
		}
		request := req.Clone(ctx)
		if metrics := modelMetrics(ctx); metrics != nil {
			metrics.Attempts++
		}
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
			if metrics := modelMetrics(ctx); metrics != nil {
				id := res.Header.Get("X-Request-ID")
				if safeCodePattern.MatchString(id) {
					metrics.RequestID = id
				}
			}
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
			if err := res.Body.Close(); err != nil {
				log.Print("model HTTP response cleanup failed")
			}
		}
		if code == "" {
			break
		}
		wait := max(delay, retryAfter)
		// Do not retry earlier than the server requested or exceed our wait bound.
		if !retry || attempt+1 >= attempts || wait > maxDelay {
			return nil, ModelDecisionError{code}
		}
		if err := observeModelRequest(ctx, ModelRequestProgress{Kind: "model_retry_wait", Attempt: attempt + 1, ErrorCode: code, RetryAt: float64(time.Now().Add(wait).UnixNano()) / 1e9}); err != nil {
			return nil, err
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
		Model string `json:"model"`
		Usage *struct {
			Input         *int64 `json:"prompt_tokens"`
			Output        *int64 `json:"completion_tokens"`
			CacheHit      *int64 `json:"prompt_cache_hit_tokens"`
			PromptDetails *struct {
				Cached *int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionDetails *struct {
				Reasoning *int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if metrics := modelMetrics(ctx); metrics != nil {
		metrics.EstimatedOutputTokens = int64(len(body))
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Choices) == 0 {
		return nil, invalidModelOutput(ctx, "model_response_invalid")
	}
	if metrics := modelMetrics(ctx); metrics != nil {
		if len(envelope.Model) <= 128 {
			metrics.ResponseModel = envelope.Model
		}
		metrics.FinishReason = envelope.Choices[0].FinishReason
		if len(metrics.FinishReason) > 32 {
			metrics.FinishReason = "unknown"
		}
		metrics.EstimatedOutputTokens = int64(len(envelope.Choices[0].Message.Content))
		if usage := envelope.Usage; usage != nil && usage.Input != nil && usage.Output != nil && *usage.Input >= 0 && *usage.Output >= 0 && *usage.Input <= 1000000000 && *usage.Output <= 1000000000 {
			metrics.UsageAvailable = true
			metrics.InputTokens, metrics.OutputTokens = *usage.Input, *usage.Output
			cached := usage.CacheHit
			if usage.PromptDetails != nil && usage.PromptDetails.Cached != nil {
				cached = usage.PromptDetails.Cached
			}
			if cached != nil && *cached >= 0 && *cached <= metrics.InputTokens {
				metrics.CachedInputTokens = cached
			}
			if usage.CompletionDetails != nil {
				n := usage.CompletionDetails.Reasoning
				if n != nil && *n >= 0 && *n <= metrics.OutputTokens {
					metrics.ReasoningOutputTokens = n
				}
			}
			priced := m.PricesConfigured || m.InputPricePerMillion > 0 || m.OutputPricePerMillion > 0
			if m.InputPricePerMillion >= 0 && m.OutputPricePerMillion >= 0 && priced && (m.CachedInputPricePerMillion == nil || *m.CachedInputPricePerMillion >= 0 && metrics.CachedInputTokens != nil) {
				inputCost := float64(metrics.InputTokens) * m.InputPricePerMillion
				if m.CachedInputPricePerMillion != nil {
					inputCost = float64(metrics.InputTokens-*metrics.CachedInputTokens)*m.InputPricePerMillion + float64(*metrics.CachedInputTokens)*(*m.CachedInputPricePerMillion)
				}
				cost := (inputCost + float64(metrics.OutputTokens)*m.OutputPricePerMillion) / 1e6
				if !math.IsNaN(cost) && !math.IsInf(cost, 0) {
					metrics.EstimatedCostUSD = &cost
				}
			}
		}
	}
	if envelope.Choices[0].Message.Refusal != "" || envelope.Choices[0].FinishReason == "content_filter" {
		return nil, ModelDecisionError{"model_refused"}
	}
	if envelope.Choices[0].FinishReason == "length" {
		return nil, ModelDecisionError{"model_output_truncated"}
	}
	if strings.TrimSpace(envelope.Choices[0].Message.Content) == "" {
		// Some compatible gateways report stop even when reasoning consumed the
		// whole allowance. Only infer exhaustion from a known request limit and
		// a sane reported output count; an ordinary empty response stays invalid.
		if usage := envelope.Usage; outputLimit > 0 && usage != nil && usage.Output != nil && *usage.Output >= outputLimit && *usage.Output <= 1000000000 {
			return nil, ModelDecisionError{"model_output_truncated"}
		}
		return nil, invalidModelOutput(ctx, "model_output_empty")
	}
	return unwrapModelJSON([]byte(envelope.Choices[0].Message.Content)), nil
}

// unwrapModelJSON removes only a complete outer Markdown fence. It does not
// extract JSON from prose, repair truncated output, or change the enclosed data.
func unwrapModelJSON(raw []byte) []byte {
	body := bytes.TrimSpace(raw)
	firstNewline := bytes.IndexByte(body, '\n')
	if firstNewline < 0 {
		return body
	}
	opening := strings.TrimSpace(string(body[:firstNewline]))
	if opening != "```" && !strings.EqualFold(opening, "```json") {
		return body
	}
	lastNewline := bytes.LastIndexByte(body, '\n')
	if lastNewline <= firstNewline || string(bytes.TrimSpace(body[lastNewline+1:])) != "```" {
		return body
	}
	return bytes.TrimSpace(body[firstNewline+1 : lastNewline])
}

// modelJSONFormatError keeps model decisions and memory proposals unambiguous.
// JSON syntax must be complete before token scanning, including nested objects.
func modelJSONFormatError(body []byte) string {
	if !json.Valid(body) {
		return modelContentError(body)
	}
	if bytes.TrimSpace(body)[0] != '{' {
		return "model_decision_schema_invalid"
	}
	type objectKeys struct {
		seen       map[string]bool
		expectsKey bool
	}
	var stack []objectKeys
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return ""
		}
		if err != nil {
			return "model_output_invalid_json"
		}
		if len(stack) > 0 {
			current := &stack[len(stack)-1]
			if key, ok := token.(string); ok && current.expectsKey {
				if current.seen[key] {
					return "model_output_duplicate_key"
				}
				current.seen[key], current.expectsKey = true, false
				continue
			}
			current.expectsKey = current.seen != nil
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				stack = append(stack, objectKeys{seen: map[string]bool{}, expectsKey: true})
			case '[':
				stack = append(stack, objectKeys{})
			case '}', ']':
				stack = stack[:len(stack)-1]
			}
		}
	}
}

func invalidModelOutput(ctx context.Context, detail string) error {
	if metrics := modelMetrics(ctx); metrics != nil {
		metrics.FormatError = detail
	}
	return ModelDecisionError{"model_decision_invalid"}
}

func modelContentError(body []byte) string {
	if !json.Valid(body) {
		if strings.Contains(string(body), "DSML") {
			return "model_output_protocol_mismatch"
		}
		return "model_output_invalid_json"
	}
	return "model_decision_schema_invalid"
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

// Close is a no-op; the adapter does not own the supplied HTTP transport.
func (m *HTTPJSONDecisionModel) Close() error { return nil }
