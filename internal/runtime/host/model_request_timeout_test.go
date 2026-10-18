package host

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletionReviewGetsIndependentModelRequestTimeout(t *testing.T) {
	provider := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {
		Name: "records.get", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"},
	}}}
	h := testHost(t, testStore(t), provider, &hostModel{})
	h.Settings.ModelTimeoutSeconds = 0.3
	h.Settings.MaxRunSeconds = 10
	h.Model = decisionModelFunc(func(ctx context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if len(packet.Facts) == 0 {
			return callDecision("records.get"), nil
		}
		timer := time.NewTimer(180 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return agentcontract.Decision{}, ctx.Err()
		case <-timer.C:
		}
		if packet.CompletionReview != nil {
			return agentcontract.Decision{Kind: "approve_completion"}, nil
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Record updated", FactIDs: []string{packet.Facts[0].FactID}}, nil
	})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || provider.calls != 1 {
		t.Fatalf("individually bounded generation/review failed or replayed write: status=%s calls=%d err=%v", run.Status, provider.calls, err)
	}
	state, err := h.Restore(run)
	if err != nil || state.AnswerMarkdown != "Record updated" {
		t.Fatal("verified answer was not persisted", err)
	}
	telemetry, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
	if err != nil || telemetry.Budget.UsageByPurpose["completion_review"].Requests != 1 {
		t.Fatal("review lost model request accounting", err)
	}
}

func TestCompletionReviewCannotExtendRunDeadline(t *testing.T) {
	// Simulated time keeps SQLite setup and race-detector scheduling from using
	// the run budget before the completion review can exercise its deadline.
	synctest.Test(t, func(t *testing.T) {
		provider := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {
			Name: "records.get", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"},
		}}}
		h := testHost(t, testStore(t), provider, &hostModel{})
		h.Settings.ModelTimeoutSeconds = 0.3
		h.Settings.MaxRunSeconds = 0.25
		reviewStarted := false
		h.Model = decisionModelFunc(func(ctx context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
			if len(packet.Facts) == 0 {
				return callDecision("records.get"), nil
			}
			if packet.CompletionReview != nil {
				reviewStarted = true
			}
			timer := time.NewTimer(180 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return agentcontract.Decision{}, ctx.Err()
			case <-timer.C:
			}
			if packet.CompletionReview != nil {
				return agentcontract.Decision{Kind: "approve_completion"}, nil
			}
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Record updated", FactIDs: []string{packet.Facts[0].FactID}}, nil
		})
		run := createTestHostRun(t, h)
		started := time.Now()
		run, err := h.Drive(t.Context(), run.RunID, "alice")
		elapsed := time.Since(started)
		state, restoreErr := h.Restore(run)
		if err != nil || restoreErr != nil || run.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "run_deadline_exceeded" || state.AnswerMarkdown != "" || provider.calls != 1 || !reviewStarted || elapsed != 250*time.Millisecond {
			t.Fatalf("review extended run deadline or published draft: status=%s state=%+v calls=%d review=%t elapsed=%v err=%v restore=%v", run.Status, state, provider.calls, reviewStarted, elapsed, err, restoreErr)
		}
	})
}

func TestIndependentModelTimeoutKeepsParentCancellation(t *testing.T) {
	provider := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {
		Name: "records.get", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"},
	}}}
	h := testHost(t, testStore(t), provider, &hostModel{})
	h.Settings.ModelTimeoutSeconds = 0.3
	h.Settings.MaxRunSeconds = 10
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h.Model = decisionModelFunc(func(modelCtx context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if len(packet.Facts) == 0 {
			return callDecision("records.get"), nil
		}
		if packet.CompletionReview != nil {
			cancel()
			<-modelCtx.Done()
			return agentcontract.Decision{}, modelCtx.Err()
		}
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Record updated", FactIDs: []string{packet.Facts[0].FactID}}, nil
	})
	run := createTestHostRun(t, h)
	_, err := h.Drive(ctx, run.RunID, "alice")
	if !errors.Is(err, context.Canceled) || provider.calls != 1 {
		t.Fatal("per-request timeout detached from caller cancellation", err, provider.calls)
	}
}

func TestModelRequestTimeoutRejectsLateSuccessfulDecision(t *testing.T) {
	provider := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {
		Name: "records.get", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"},
	}}}
	h := testHost(t, testStore(t), provider, &hostModel{})
	h.Settings.ModelTimeoutSeconds = 0.05
	h.Settings.MaxRunSeconds = 10
	h.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
		if len(packet.Facts) == 0 {
			return callDecision("records.get"), nil
		}
		// A caller-supplied implementation may return a valid response after its
		// deadline. The runtime must still reject publication without replay.
		timer := time.NewTimer(80 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		return agentcontract.Decision{Kind: "final", AnswerMarkdown: "Record updated", FactIDs: []string{packet.Facts[0].FactID}}, nil
	})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	state, restoreErr := h.Restore(run)
	if err != nil || restoreErr != nil || run.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "model_timeout" || state.AnswerMarkdown != "" || provider.calls != 1 {
		t.Fatalf("late model response was published or replayed: status=%s calls=%d err=%v restore=%v", run.Status, provider.calls, err, restoreErr)
	}
}
