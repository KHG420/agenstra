package modelapi

import (
	"context"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// ExtractMemories validates proposals derived from the supplied user input.
func (m *HTTPJSONDecisionModel) ExtractMemories(ctx context.Context, request agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
	proposals, _, err := m.ExtractMemoriesMeasured(ctx, request)
	return proposals, err
}

// ExtractMemoriesMeasured retains model usage even when extraction fails.
func (m *HTTPJSONDecisionModel) ExtractMemoriesMeasured(ctx context.Context, request agentcontract.MemoryExtractionRequest) (proposals []agentcontract.MemoryProposal, metrics agentcontract.ModelCallMetrics, resultErr error) {
	started := time.Now()
	ctx = context.WithValue(ctx, modelMetricsKey{}, &metrics)
	ctx = context.WithValue(ctx, modelTokenBudgetKey{}, request.ModelTokensRemaining)
	defer func() {
		metrics.ElapsedMilliseconds = time.Since(started).Milliseconds()
		if resultErr != nil {
			metrics.ErrorCode = agentcontract.Strptr(agentcontract.ModelErrorCode(resultErr))
		}
	}()
	input, err := agentcontract.MemoryExtractionInput(&request)
	if err != nil {
		return nil, metrics, err
	}
	model := *m
	if request.ContextWindowTokens > 0 && (model.ContextWindowTokens == 0 || request.ContextWindowTokens < model.ContextWindowTokens) {
		model.ContextWindowTokens = request.ContextWindowTokens
	}
	if request.MaxInputTokens > 0 && (model.MaxInputTokens == 0 || request.MaxInputTokens < model.MaxInputTokens) {
		model.MaxInputTokens = request.MaxInputTokens
	}
	model.ProtocolReserveTokens = max(model.ProtocolReserveTokens, request.ProtocolReserveTokens)
	if request.MaxOutputTokens > 0 && (model.MaxOutputTokens <= 0 || request.MaxOutputTokens < model.MaxOutputTokens) {
		model.MaxOutputTokens = request.MaxOutputTokens
	}
	raw, err := model.requestJSON(ctx, input, agentcontract.MemoryExtractionPrompt)
	if err != nil {
		return nil, metrics, err
	}
	var response struct {
		Proposals []agentcontract.MemoryProposal `json:"proposals"`
	}
	if detail := modelJSONFormatError(raw); detail != "" {
		metrics.FormatError = detail
		return nil, metrics, agentcontract.NewHostError("memory_extraction_invalid")
	}
	if err = jsonvalue.DecodeStrict(raw, &response); err != nil || response.Proposals == nil {
		metrics.FormatError = "model_memory_schema_invalid"
		return nil, metrics, agentcontract.NewHostError("memory_extraction_invalid")
	}
	if err = agentcontract.ValidateProposals(request.Text, response.Proposals); err != nil {
		return nil, metrics, err
	}
	return response.Proposals, metrics, nil
}
