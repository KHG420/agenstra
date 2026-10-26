package agent

import "context"

// AnswerUpdate is a replacement snapshot, never a delta or an execution receipt.
// Only the host may publish completed after the validated answer is persisted.
type AnswerUpdate struct {
	RunID string `json:"run_id"`
	Text  string `json:"text"`
	Phase string `json:"phase"`
}

type answerObserverKey struct{}

// WithAnswerObserver binds a synchronous, nonblocking candidate observer to one
// execution context. Runtime and host retain validation and publication authority.
func WithAnswerObserver(ctx context.Context, observer func(AnswerUpdate)) context.Context {
	return context.WithValue(ctx, answerObserverKey{}, observer)
}

// ObserveAnswer reports a candidate without granting publication authority.
func ObserveAnswer(ctx context.Context, update AnswerUpdate) {
	if ctx.Err() != nil {
		return
	}
	if observer, ok := ctx.Value(answerObserverKey{}).(func(AnswerUpdate)); ok {
		observer(update)
	}
}
