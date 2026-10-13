package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestModelErrorCodePreservesDeadlineAndDeliberateCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "nil"},
		{name: "deadline", err: context.DeadlineExceeded, want: "model_timeout"},
		{name: "wrapped deadline", err: fmt.Errorf("private gateway path: %w", context.DeadlineExceeded), want: "model_timeout"},
		{name: "cancel is not a timeout", err: context.Canceled, want: "model_unavailable"},
		{name: "text does not prove deadline", err: errors.New("deadline exceeded at private gateway"), want: "model_unavailable"},
		{name: "deliberate code wins", err: errors.Join(ModelDecisionError{Kind: "model_refused"}, context.DeadlineExceeded), want: "model_refused"},
		{name: "unknown private error", err: errors.New("private upstream credentials"), want: "model_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelErrorCode(tc.err); got != tc.want {
				t.Fatalf("safe model code=%q want=%q", got, tc.want)
			}
		})
	}
}
