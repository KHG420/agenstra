package host

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

// Preparation shares the run's lease, deadline, cancellation and checkpoint.
// Originals are read one bounded range at a time, including for legacy chats.
func (h *AgentHost) prepareConversation(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, runtime *reactcore.AgentRuntime) (agentcontract.StoredRun, error) {
	if h.ConversationHistory == nil {
		return run, nil
	}
	if state.Conversation == nil {
		seed, err := h.ConversationHistory.ConversationSeed(ctx, run)
		if err != nil || seed == nil {
			return run, err
		}
		state.Conversation = seed
		state.Status = "running"
		run, err = h.save(run, state, "", nil, agentcontract.JSON{"kind": "conversation_loading"})
		if err != nil {
			return run, err
		}
	}
	for !state.Conversation.Ready {
		if err := ctx.Err(); err != nil {
			return run, err
		}
		page, err := h.ConversationHistory.ConversationPage(ctx, run, state.Conversation.Cursor, 4000)
		if err != nil {
			return run, err
		}
		if page.Entry == nil {
			state.Conversation.Ready = true
			state.Conversation.Cursor = page.Next
		} else {
			if page.Next == state.Conversation.Cursor || page.Entry.ID == "" {
				return run, agentcontract.NewHostError("conversation_cursor_invalid")
			}
			state.Conversation.Recent = append(state.Conversation.Recent, *page.Entry)
			state.Conversation.Cursor = page.Next
		}
		run, err = h.save(run, state, "", nil, nil)
		if err != nil {
			return run, err
		}
		run, err = h.fitConversation(ctx, run, state, runtime)
		if err != nil {
			return run, err
		}
	}
	return h.fitConversation(ctx, run, state, runtime)
}

func (h *AgentHost) fitConversation(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, runtime *reactcore.AgentRuntime) (agentcontract.StoredRun, error) {
	if state.Conversation == nil {
		return run, nil
	}
	for {
		fits, err := conversationFits(state, runtime, true)
		if err != nil || fits {
			return run, err
		}
		if len(state.Conversation.Recent) == 0 {
			fits, err = conversationFits(state, runtime, false)
			if err != nil || fits {
				return run, err
			}
			return run, agentcontract.NewHostError("conversation_context_exhausted")
		}
		run, err = h.compactConversation(ctx, run, state, runtime.Model)
		if err != nil {
			return run, err
		}
	}
}

func conversationFits(state *agentcontract.RuntimeState, runtime *reactcore.AgentRuntime, headroom bool) (bool, error) {
	packet := runtime.Context(state)
	prompt := runtime.SystemPrompt()
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil || packet.Schema == "" {
		return false, agentcontract.NewHostError("run_state_invalid")
	}
	// Leave room for new evidence, decision feedback and protocol overhead.
	characters := runtime.MaxContextCharacters
	if headroom {
		characters -= min(8000, characters/8)
	}
	if characters > 0 && utf8.RuneCount(raw)+utf8.RuneCountInString(prompt) > characters {
		return false, nil
	}
	info := agentcontract.DescribeModel(runtime.Model)
	limit := runtime.MaxModelInputTokens
	if info.MaxInputTokens != nil && (limit == 0 || *info.MaxInputTokens < limit) {
		limit = *info.MaxInputTokens
	}
	window := runtime.ModelContextWindowTokens
	if info.ContextWindowTokens != nil && (window == 0 || *info.ContextWindowTokens < window) {
		window = *info.ContextWindowTokens
	}
	reserve := int64(max(runtime.ModelOutputReserveTokens, runtime.MaxModelOutputTokens))
	if info.MaxOutputTokens != nil && (reserve == 0 || *info.MaxOutputTokens < reserve) {
		reserve = *info.MaxOutputTokens
	}
	if window > 0 {
		available := window - reserve - max(runtime.ModelProtocolReserveTokens, info.ProtocolReserveTokens)
		if available <= 0 {
			return false, agentcontract.NewHostError("context_too_large")
		}
		if limit == 0 || available < limit {
			limit = available
		}
	}
	if limit == 0 {
		return true, nil
	}
	packet.MaxModelInputTokens = limit
	measurement := agentcontract.InputMeasurement{Tokens: int64(len(raw) + len(prompt) + 128)}
	if measurer, ok := runtime.Model.(agentcontract.ModelInputMeasurer); ok {
		measurement, err = measurer.MeasureInput(packet, prompt)
		if err != nil {
			return false, err
		}
	}
	if headroom {
		limit -= min(8000, limit/8)
	}
	return measurement.Tokens <= limit, nil
}

func (h *AgentHost) compactConversation(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, model agentcontract.DecisionModel) (agentcontract.StoredRun, error) {
	compactor, ok := model.(agentcontract.ConversationCompactor)
	if !ok {
		return run, agentcontract.NewHostError("conversation_compaction_unavailable")
	}
	c := state.Conversation
	s := h.runSettings(run)
	count := min(48, max(1, len(c.Recent)*3/4))
	request := agentcontract.ConversationCompactionRequest{
		Summary: c.Summary, Requirements: c.Requirements, MaxSummaryCharacters: min(6000, s.MaxContextCharacters/8),
		MaxCharacters: s.MaxContextCharacters, MaxInputTokens: s.MaxModelInputTokens, ContextWindowTokens: s.ModelContextWindowTokens,
		ProtocolReserveTokens: s.ModelProtocolReserveTokens, MaxOutputTokens: s.MaxModelOutputTokens,
	}
	info := agentcontract.DescribeModel(model)
	if info.MaxOutputTokens != nil && (request.MaxOutputTokens == 0 || *info.MaxOutputTokens < int64(request.MaxOutputTokens)) {
		request.MaxOutputTokens = int(*info.MaxOutputTokens)
	}
	if info.ContextWindowTokens != nil && (request.ContextWindowTokens == 0 || *info.ContextWindowTokens < request.ContextWindowTokens) {
		request.ContextWindowTokens = *info.ContextWindowTokens
	}
	if info.MaxInputTokens != nil && (request.MaxInputTokens == 0 || *info.MaxInputTokens < request.MaxInputTokens) {
		request.MaxInputTokens = *info.MaxInputTokens
	}
	request.ProtocolReserveTokens = max(request.ProtocolReserveTokens, info.ProtocolReserveTokens)
	if request.ContextWindowTokens > 0 {
		available := request.ContextWindowTokens - int64(request.MaxOutputTokens) - request.ProtocolReserveTokens
		if available <= 0 {
			return run, agentcontract.NewHostError("context_too_large")
		}
		if request.MaxInputTokens == 0 || available < request.MaxInputTokens {
			request.MaxInputTokens = available
		}
	}
	var measurement agentcontract.InputMeasurement
	for count > 0 {
		request.Messages = c.Recent[:count]
		var err error
		measurement, err = compactor.MeasureConversationInput(request)
		if err != nil {
			return run, err
		}
		// The selected adapter also enforces its profile window at dispatch.
		if request.MaxInputTokens <= 0 || measurement.Tokens <= request.MaxInputTokens {
			break
		}
		count /= 2
	}
	if count == 0 {
		return run, agentcontract.NewHostError("conversation_context_exhausted")
	}
	for c.Failures < 3 {
		if err := ctx.Err(); err != nil {
			return run, err
		}
		remaining := int64(0)
		if s.MaxModelTokens > 0 {
			remaining = max(0, s.MaxModelTokens-state.ModelUsage.BudgetTokens)
			if remaining <= measurement.Tokens {
				return run, agentcontract.NewHostError("model_token_budget_exhausted")
			}
		}
		request.ModelTokensRemaining = remaining
		reservation := agentcontract.ModelCallMetrics{Purpose: "conversation_compaction", SourceID: request.Messages[count-1].ID, Reservation: true, Attempts: 1, EstimatedInputTokens: measurement.Tokens, EstimatedOutputTokens: int64(request.MaxOutputTokens), ErrorCode: agentcontract.Strptr("model_outcome_unknown")}
		c.Failures++
		state.Status = "running"
		reactcore.RecordModelCall(state, reservation)
		index := len(state.ModelCalls) - 1
		var err error
		run, err = h.save(run, state, "", nil, agentcontract.JSON{"kind": "conversation_compaction_requested", "version": c.Compactions + 1})
		if err != nil {
			return run, err
		}
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(s.ModelTimeoutSeconds*1e9))
		callCtx = agentcontract.WithModelRequestObserver(callCtx, func(progress agentcontract.ModelRequestProgress) error {
			var saveErr error
			run, saveErr = h.save(run, state, "", nil, agentcontract.JSON{"kind": progress.Kind, "purpose": "conversation_compaction", "attempt": progress.Attempt, "error_code": progress.ErrorCode, "retry_at": progress.RetryAt})
			return saveErr
		})
		input, cloneErr := jsonvalue.Clone(request)
		if cloneErr != nil {
			cancel()
			return run, agentcontract.NewHostError("run_state_invalid")
		}
		// JSON cloning copies mutable data, while dispatch limits stay private.
		input.MaxCharacters, input.MaxInputTokens, input.ContextWindowTokens = request.MaxCharacters, request.MaxInputTokens, request.ContextWindowTokens
		input.ProtocolReserveTokens, input.MaxOutputTokens, input.ModelTokensRemaining = request.ProtocolReserveTokens, request.MaxOutputTokens, remaining
		result, metrics, callErr := compactor.CompactConversation(callCtx, input)
		cancel()
		metrics.Purpose, metrics.SourceID = reservation.Purpose, reservation.SourceID
		metrics.Reservation = false
		if callErr != nil {
			metrics.ErrorCode = agentcontract.Strptr(agentcontract.ModelErrorCode(callErr))
			if !metrics.UsageAvailable && metrics.Attempts > 0 && (errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded)) {
				metrics.Reservation = true
				metrics.EstimatedInputTokens, metrics.EstimatedOutputTokens = reservation.EstimatedInputTokens, reservation.EstimatedOutputTokens
			}
		}
		var requirements []agentcontract.ConversationRequirement
		if callErr == nil {
			requirements, callErr = agentcontract.ValidateConversationSummary(request, result)
			if callErr != nil {
				metrics.ErrorCode = agentcontract.Strptr("conversation_compaction_invalid")
			}
		}
		state.ModelCalls[index] = metrics
		reactcore.RebuildModelUsage(state)
		if callErr == nil {
			c.Summary, c.Requirements = result.Summary, requirements
			c.SummaryThrough = request.Messages[count-1].ID
			c.Recent = append([]agentcontract.ConversationEntry{}, c.Recent[count:]...)
			c.Compactions++
			c.Failures = 0
		}
		run, err = h.save(run, state, "", nil, agentcontract.JSON{"kind": "conversation_compacted", "version": c.Compactions, "summary_through": c.SummaryThrough, "covered_ids": result.CoveredIDs, "metrics": metrics})
		if err != nil {
			return run, err
		}
		if ctx.Err() != nil {
			return run, ctx.Err()
		}
		if s.MaxModelTokens > 0 && state.ModelUsage.BudgetTokens > s.MaxModelTokens {
			return run, agentcontract.NewHostError("model_token_budget_exhausted")
		}
		if callErr == nil {
			return run, nil
		}
		if agentcontract.ModelErrorCode(callErr) != "conversation_compaction_invalid" {
			return run, callErr
		}
	}
	return run, agentcontract.NewHostError("conversation_compaction_failed")
}
