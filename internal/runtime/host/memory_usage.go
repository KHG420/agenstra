package host

import (
	"context"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func (h *AgentHost) extractRunMemories(ctx context.Context, run agentcontract.StoredRun, request agentcontract.MemoryExtractionRequest, sourceID string) (agentcontract.StoredRun, []agentcontract.MemoryProposal, error) {
	runModel, modelErr := h.modelForRun(run)
	if modelErr != nil {
		return run, nil, modelErr
	}
	state, err := h.Restore(run)
	if err != nil {
		return run, nil, err
	}
	input, err := agentcontract.MemoryExtractionInput(&request)
	if err != nil {
		return run, nil, err
	}
	s := h.runSettings(run)
	remaining := int64(0)
	if s.MaxModelTokens > 0 {
		remaining = max(int64(0), s.MaxModelTokens-state.ModelUsage.BudgetTokens)
		if remaining <= int64(len(input)+len(agentcontract.MemoryExtractionPrompt)+128) {
			return run, nil, agentcontract.NewHostError("model_token_budget_exhausted")
		}
	}
	reservation := agentcontract.ModelCallMetrics{Purpose: "memory_extraction", SourceID: sourceID, Reservation: true, Attempts: 1, EstimatedInputTokens: int64(len(input) + len(agentcontract.MemoryExtractionPrompt) + 128), EstimatedOutputTokens: int64(s.MaxModelOutputTokens), ErrorCode: agentcontract.Strptr("model_outcome_unknown")}
	if remaining > 0 {
		reservation.EstimatedInputTokens = remaining
		reservation.EstimatedOutputTokens = 0
	}

	// Extraction is active model work even before the decision loop starts.
	state.Status = "running"
	reactcore.RecordModelCall(state, reservation)
	index := len(state.ModelCalls) - 1
	run, err = h.save(run, state, "", nil, agentcontract.JSON{"kind": "memory_requested", "source_id": sourceID})
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
	extractCtx = agentcontract.WithModelRequestObserver(extractCtx, func(progress agentcontract.ModelRequestProgress) error {
		var e error
		run, e = h.save(run, state, "", nil, agentcontract.JSON{"kind": progress.Kind, "purpose": "memory_extraction", "attempt": progress.Attempt, "error_code": progress.ErrorCode, "retry_at": progress.RetryAt})
		return e
	})
	started := time.Now()
	var proposals []agentcontract.MemoryProposal
	metrics := agentcontract.ModelCallMetrics{Attempts: 1, EstimatedInputTokens: int64(len(input) + len(agentcontract.MemoryExtractionPrompt) + 128)}
	if measured, ok := runModel.(agentcontract.MeasuredMemoryExtractor); ok {
		proposals, metrics, err = measured.ExtractMemoriesMeasured(extractCtx, request)
	} else {
		proposals, err = runModel.(agentcontract.MemoryExtractor).ExtractMemories(extractCtx, request)
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
		metrics.ErrorCode = agentcontract.Strptr(agentcontract.ModelErrorCode(err))
	}
	state.ModelCalls[index] = metrics
	reactcore.RebuildModelUsage(state)
	if s.MaxModelTokens > 0 && state.ModelUsage.BudgetTokens > s.MaxModelTokens {
		err = agentcontract.NewHostError("model_token_budget_exhausted")
		proposals = nil
	}
	saved, saveErr := h.save(run, state, "", nil, agentcontract.JSON{"kind": "memory_decided", "source_id": sourceID, "metrics": metrics})
	if saveErr != nil {
		return run, nil, saveErr
	}
	return saved, proposals, err
}
