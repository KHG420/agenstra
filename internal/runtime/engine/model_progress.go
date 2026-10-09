package engine

import "context"

// ModelRequestProgress reports transport attempts and retry timing without upstream response text.
type ModelRequestProgress struct {
	Kind      string  `json:"kind"`
	Attempt   int     `json:"attempt"`
	ErrorCode string  `json:"error_code,omitempty"`
	RetryAt   float64 `json:"retry_at,omitempty"`
}
type modelProgressKey struct{}

// WithModelRequestObserver lets embedded hosts observe retries without replacing
// DecisionModel. Callback failure prevents the next transport attempt.
func WithModelRequestObserver(ctx context.Context, observer func(ModelRequestProgress) error) context.Context {
	return context.WithValue(ctx, modelProgressKey{}, observer)
}
func observeModelRequest(ctx context.Context, p ModelRequestProgress) error {
	if observer, ok := ctx.Value(modelProgressKey{}).(func(ModelRequestProgress) error); ok {
		return observer(p)
	}
	return nil
}
