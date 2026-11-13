package modelapi

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// CompactConversation returns a source-validated summary and all received usage.
func (m *HTTPJSONDecisionModel) CompactConversation(ctx context.Context, request agentcontract.ConversationCompactionRequest) (result agentcontract.ConversationSummary, metrics agentcontract.ModelCallMetrics, resultErr error) {
	started := time.Now()
	ctx = context.WithValue(ctx, modelMetricsKey{}, &metrics)
	ctx = context.WithValue(ctx, modelTokenBudgetKey{}, request.ModelTokensRemaining)
	defer func() {
		metrics.ElapsedMilliseconds = time.Since(started).Milliseconds()
		if resultErr != nil {
			metrics.ErrorCode = agentcontract.Strptr(agentcontract.ModelErrorCode(resultErr))
		}
	}()
	input, err := conversationInput(request)
	if err != nil {
		return result, metrics, err
	}
	model := m.conversationRequestModel(request)
	raw, err := model.requestJSON(ctx, input, agentcontract.ConversationCompactionPrompt)
	if err != nil {
		return result, metrics, err
	}
	if detail := modelJSONFormatError(raw); detail != "" {
		metrics.FormatError = detail
		return result, metrics, agentcontract.NewHostError("conversation_compaction_invalid")
	}
	if err = jsonvalue.DecodeStrict(raw, &result); err != nil {
		metrics.FormatError = "model_conversation_schema_invalid"
		return result, metrics, agentcontract.NewHostError("conversation_compaction_invalid")
	}
	if _, err = agentcontract.ValidateConversationSummary(request, result); err != nil {
		metrics.FormatError = "model_conversation_sources_invalid"
		return result, metrics, err
	}
	return result, metrics, nil
}

// MeasureConversationInput measures the exact envelope sent by CompactConversation.
func (m *HTTPJSONDecisionModel) MeasureConversationInput(request agentcontract.ConversationCompactionRequest) (agentcontract.InputMeasurement, error) {
	input, err := conversationInput(request)
	if err != nil {
		return agentcontract.InputMeasurement{}, err
	}
	model := m.conversationRequestModel(request)
	payload, err := model.requestPayload(input, agentcontract.ConversationCompactionPrompt)
	if err != nil {
		return agentcontract.InputMeasurement{}, err
	}
	raw, err := agentcontract.CanonicalJSON(payload)
	if err != nil {
		return agentcontract.InputMeasurement{}, err
	}
	return model.measurePayload(raw)
}

func conversationInput(request agentcontract.ConversationCompactionRequest) ([]byte, error) {
	input, err := agentcontract.CanonicalJSON(request)
	if err != nil {
		return nil, agentcontract.NewHostError("run_state_invalid")
	}
	if request.MaxCharacters > 0 && utf8.RuneCount(input)+utf8.RuneCountInString(agentcontract.ConversationCompactionPrompt) > request.MaxCharacters {
		return nil, agentcontract.NewHostError("context_too_large")
	}
	return input, nil
}

func (m *HTTPJSONDecisionModel) conversationRequestModel(request agentcontract.ConversationCompactionRequest) HTTPJSONDecisionModel {
	model := m.memoryRequestModel(agentcontract.MemoryExtractionRequest{
		ContextWindowTokens: request.ContextWindowTokens, MaxInputTokens: request.MaxInputTokens,
		ProtocolReserveTokens: request.ProtocolReserveTokens, MaxOutputTokens: request.MaxOutputTokens,
	})
	// Compaction returns data-only JSON and never exposes partial summary text.
	model.DecisionOutputMode = "json_object"
	model.StreamAnswers = false
	return model
}
