package agenstra

import (
	"context"
	"time"
)

// MeasuredMemoryExtractor is optional; existing custom extractors still work.
type MeasuredMemoryExtractor interface {
	ExtractMemoriesMeasured(context.Context, MemoryExtractionRequest) ([]MemoryProposal, ModelCallMetrics, error)
}

func (h *AgentHost) extractRunMemories(ctx context.Context, run StoredRun, request MemoryExtractionRequest, sourceID string) (StoredRun, []MemoryProposal, error) {
	runModel, modelErr := h.modelForRun(run)
	if modelErr != nil {
		return run, nil, modelErr
	}
	state, err := h.restore(run)
	if err != nil {
		return run, nil, err
	}
	input, err := memoryExtractionInput(&request)
	if err != nil {
		return run, nil, err
	}
	s := h.runSettings(run)
	remaining := int64(0)
	if s.MaxModelTokens > 0 {
		remaining = max(int64(0), s.MaxModelTokens-state.ModelUsage.BudgetTokens)
		if remaining <= int64(len(input)+len(memoryExtractionPrompt)+128) {
			return run, nil, hostError("model_token_budget_exhausted")
		}
	}
	reservation := ModelCallMetrics{Purpose: "memory_extraction", SourceID: sourceID, Reservation: true, Attempts: 1, EstimatedInputTokens: int64(len(input) + len(memoryExtractionPrompt) + 128), EstimatedOutputTokens: int64(s.MaxModelOutputTokens), ErrorCode: strptr("model_outcome_unknown")}
	if remaining > 0 {
		reservation.EstimatedInputTokens = remaining
		reservation.EstimatedOutputTokens = 0
	}
	// Extraction is active model work even before the decision loop starts.
	state.Status = "running"
	recordModelCall(state, reservation)
	index := len(state.ModelCalls) - 1
	run, err = h.save(run, state, "", nil, JSON{"kind": "memory_requested", "source_id": sourceID})
	if err != nil {
		return run, nil, err
	}
	request.ModelTokensRemaining = remaining
	request.MaxOutputTokens = s.MaxModelOutputTokens
	if s.ModelOutputReserveTokens > 0 && (request.MaxOutputTokens == 0 || s.ModelOutputReserveTokens < request.MaxOutputTokens) {
		request.MaxOutputTokens = s.ModelOutputReserveTokens
	}
	request.ContextWindowTokens = s.ModelContextWindowTokens
	request.MaxInputTokens = s.MaxModelInputTokens
	request.ProtocolReserveTokens = s.ModelProtocolReserveTokens
	extractCtx, cancel := context.WithTimeout(ctx, time.Duration(s.ModelTimeoutSeconds*1e9))
	defer cancel()
	extractCtx = WithModelRequestObserver(extractCtx, func(progress ModelRequestProgress) error {
		var e error
		run, e = h.save(run, state, "", nil, JSON{"kind": progress.Kind, "purpose": "memory_extraction", "attempt": progress.Attempt, "error_code": progress.ErrorCode, "retry_at": progress.RetryAt})
		return e
	})
	started := time.Now()
	var proposals []MemoryProposal
	metrics := ModelCallMetrics{Attempts: 1, EstimatedInputTokens: int64(len(input) + len(memoryExtractionPrompt) + 128)}
	if measured, ok := runModel.(MeasuredMemoryExtractor); ok {
		proposals, metrics, err = measured.ExtractMemoriesMeasured(extractCtx, request)
	} else {
		proposals, err = runModel.(MemoryExtractor).ExtractMemories(extractCtx, request)
	}
	if ctx.Err() != nil {
		return run, nil, ctx.Err()
	}
	metrics.Purpose = "memory_extraction"
	metrics.SourceID = sourceID
	metrics.Reservation = false
	if metrics.ElapsedMilliseconds == 0 {
		metrics.ElapsedMilliseconds = time.Since(started).Milliseconds()
	}
	if err != nil {
		metrics.ErrorCode = strptr(ErrorCode(err))
	}
	state.ModelCalls[index] = metrics
	rebuildModelUsage(state)
	if s.MaxModelTokens > 0 && state.ModelUsage.BudgetTokens > s.MaxModelTokens {
		err = hostError("model_token_budget_exhausted")
		proposals = nil
	}
	saved, saveErr := h.save(run, state, "", nil, JSON{"kind": "memory_decided", "source_id": sourceID, "metrics": metrics})
	if saveErr != nil {
		return run, nil, saveErr
	}
	return saved, proposals, err
}
