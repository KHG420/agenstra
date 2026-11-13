package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

type conversationTestModel struct {
	*hostModel
	compactions int
	onCompact   func(context.Context, agentcontract.ConversationCompactionRequest) error
}

func (m *conversationTestModel) MeasureConversationInput(r agentcontract.ConversationCompactionRequest) (agentcontract.InputMeasurement, error) {
	raw, err := agentcontract.CanonicalJSON(r)
	return agentcontract.InputMeasurement{Tokens: int64(len(raw)), Source: "test_bytes"}, err
}

func (m *conversationTestModel) CompactConversation(ctx context.Context, r agentcontract.ConversationCompactionRequest) (agentcontract.ConversationSummary, agentcontract.ModelCallMetrics, error) {
	m.compactions++
	metrics := agentcontract.ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 120, OutputTokens: 60}
	if m.onCompact != nil {
		if err := m.onCompact(ctx, r); err != nil {
			return agentcontract.ConversationSummary{}, metrics, err
		}
	}
	result := agentcontract.ConversationSummary{Summary: "报告工作持续进行。原文可按 source_id 回读。", Requirements: []agentcontract.ConversationRequirement{}, CoveredIDs: []string{}}
	for _, entry := range r.Messages {
		result.CoveredIDs = append(result.CoveredIDs, entry.ID)
		for _, quote := range []string{"本会话统一用中文", "金额使用人民币", "后续改用英文"} {
			if entry.Role != "user" || !strings.Contains(entry.Text, quote) {
				continue
			}
			requirement := agentcontract.ConversationRequirement{SourceID: entry.ID, Quote: quote}
			if quote == "后续改用英文" {
				for _, old := range r.Requirements {
					if old.Quote == "本会话统一用中文" {
						requirement.Supersedes = []string{old.ID}
					}
				}
			}
			result.Requirements = append(result.Requirements, requirement)
		}
	}
	return result, metrics, nil
}

func durableTestModel(f *webFixture) *conversationTestModel {
	f.w.Config.ConversationContext = true
	f.h.Settings.MaxContextCharacters = 48000
	f.h.Settings.MaxConversationMessages = 2
	m := &conversationTestModel{hostModel: &hostModel{}}
	f.h.Model = m
	return m
}

func TestDurableConversationOverTwoHundredTurns(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	model := durableTestModel(f)
	c, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	var first agentcontract.ChatMessage
	var last agentcontract.StoredRun
	reportedCompactions := 0
	// Import an existing transcript with no compaction checkpoints, then continue
	// through the normal chat entry. Batch setup keeps race/SQLite CI bounded.
	legacy := make([]agentcontract.ChatMessage, 0, 205)
	checkpoints := make([]string, 0, 205)
	for i := range 205 {
		text := fmt.Sprintf("继续报告，第%d条。%s", i, strings.Repeat("本轮资料和已经完成的细节。", 160))
		if i == 0 {
			text = "本会话统一用中文，金额使用人民币。原始凭证号 EARLY-751。" + text
		}
		if i == 180 {
			text = "后续改用英文。" + text
		}
		message := agentcontract.ChatMessage{ID: agentcontract.NewID(), RunID: agentcontract.NewID(), ConversationID: c.ID, ClientID: fmt.Sprint(i), Text: text, Status: "completed", AnswerMarkdown: "本轮资料已核对。", CreatedAt: f.now}
		state, err := reactcore.NewState(text, message.RunID)
		if err != nil {
			t.Fatal(err)
		}
		state.Status, state.AnswerMarkdown = message.Status, message.AnswerMarkdown
		raw, err := agentcontract.EncodeDocument(agentcontract.JSON{"runtime": state, "artifact_ids": []string{}})
		if err != nil {
			t.Fatal(err)
		}
		legacy = append(legacy, message)
		checkpoints = append(checkpoints, raw)
		if i == 0 {
			first = message
		}
	}
	if err := f.h.Store.Transaction(func(tx *sql.Tx) error {
		for i, message := range legacy {
			if _, err := tx.Exec("INSERT INTO runs(run_id,owner_id,pack_id,status,state_json,revision,created_at,updated_at) VALUES($1,'alice','records','completed',$2,0,$3,$3)", message.RunID, checkpoints[i], f.now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.w.Store.store.Transaction(func(tx *sql.Tx) error {
		for _, message := range legacy {
			raw, err := agentcontract.EncodeDocument(message)
			if err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO web_messages(id,owner,conversation,client_id,payload) VALUES($1,'alice',$2,$3,$4)", message.ID, c.ID, message.ClientID, raw); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		message, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, fmt.Sprintf("continued-%d", i), "继续完成报告，核对早期要求。", "")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
			t.Fatal(err)
		}
		last, err = f.h.Drive(t.Context(), message.RunID, "alice")
		if err != nil || last.Status != "completed" {
			t.Fatalf("turn %d: %s %v", i, last.Status, err)
		}
		checkpoint, err := f.h.Restore(last)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range checkpoint.ModelCalls {
			if call.Purpose == "conversation_compaction" && call.UsageAvailable {
				reportedCompactions++
			}
		}
		if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
			t.Fatal(err)
		}
	}
	state, err := f.h.Restore(last)
	if err != nil {
		t.Fatal(err)
	}
	context := state.Conversation
	if context == nil || !context.Ready || context.Compactions < 5 || context.SummaryThrough == "" {
		t.Fatal("compaction did not persist", context)
	}
	quotes := map[string]bool{}
	for _, r := range context.Requirements {
		quotes[r.Quote] = true
	}
	if !quotes["金额使用人民币"] || !quotes["后续改用英文"] || quotes["本会话统一用中文"] {
		t.Fatal("earlier requirements or revision lost", quotes)
	}
	if reportedCompactions != model.compactions {
		t.Fatal("compaction usage lost")
	}
	_, messages, next, err := f.w.ConversationMessages(t.Context(), "alice", c.ID, 0, 50)
	if err != nil || len(messages) != 50 || next == 0 {
		t.Fatal(len(messages), next, err)
	}
	count := len(messages)
	for next > 0 {
		_, page, before, err := f.w.ConversationMessages(t.Context(), "alice", c.ID, next, 50)
		if err != nil || len(page) == 0 || page[len(page)-1].Sequence >= next {
			t.Fatal("invalid history page", err)
		}
		count += len(page)
		next = before
	}
	if count != 208 {
		t.Fatal("original messages missing", count)
	}
	provider, err := f.h.OpenRunProvider(t.Context(), last)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	inv := &agentcontract.InvocationContext{OwnerID: "alice", RunID: last.RunID}
	result, err := provider.Invoke(t.Context(), historySearchCapability, agentcontract.JSON{"query": "EARLY-751"}, inv)
	if err != nil || result.ErrorCode != "" || !strings.Contains(fmt.Sprint(result.Data), first.ID) {
		t.Fatal("early original not searchable", result, err)
	}
	result, err = provider.Invoke(t.Context(), historyReadCapability, agentcontract.JSON{"source_id": first.ID + ":0:0"}, inv)
	if err != nil || !strings.Contains(fmt.Sprint(result.Data), "EARLY-751") {
		t.Fatal("original not readable", result, err)
	}
	inv.OwnerID = "bob"
	result, err = provider.Invoke(t.Context(), historyReadCapability, agentcontract.JSON{}, inv)
	if err != nil || result.ErrorCode != "access_denied" {
		t.Fatal("history escaped owner boundary", result, err)
	}
	if model.compactions != context.Compactions {
		t.Fatal("compaction counted inconsistently", model.compactions, context.Compactions)
	}
}

func TestDurableConversationKeepsCompleteShortHistoryAndPartialUnicodeRanges(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	model := durableTestModel(f)
	c, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, "first", "保留全文🙂完整输入", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.Drive(t.Context(), first.RunID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
		t.Fatal(err)
	}
	second, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, "second", "继续", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
		t.Fatal(err)
	}
	run, err := f.h.Drive(t.Context(), second.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.h.Restore(run)
	if err != nil || state.Conversation.Summary != "" || model.compactions != 0 || state.Conversation.Recent[0].Text != "保留全文🙂完整输入" {
		t.Fatal("short history was truncated or summarized", state, err)
	}
	cursor := agentcontract.ConversationCursor{}
	text := ""
	for {
		page, err := f.w.ConversationPage(t.Context(), run, cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		if page.Entry == nil {
			break
		}
		if page.Entry.Role == "user" {
			text += page.Entry.Text
		}
		cursor = page.Next
	}
	if text != "保留全文🙂完整输入" {
		t.Fatal("partial cursor lost Unicode original", text)
	}
}

func TestDurableConversationRecoversInterruptedCompactionAndKeepsUsage(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	model := durableTestModel(f)
	c, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	model.onCompact = func(context.Context, agentcontract.ConversationCompactionRequest) error {
		cancel()
		return context.Canceled
	}
	var interrupted agentcontract.StoredRun
	for i := range 12 {
		message, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, fmt.Sprint(i), strings.Repeat("需保留的原始材料。", 900), "")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
			t.Fatal(err)
		}
		run, err := f.h.Drive(ctx, message.RunID, "alice")
		if errors.Is(err, context.Canceled) {
			interrupted = run
			break
		}
		if err != nil || run.Status != "completed" {
			t.Fatal(run.Status, err)
		}
		if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if interrupted.RunID == "" {
		t.Fatal("did not exercise compaction interruption")
	}
	saved, err := f.h.Get(t.Context(), interrupted.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.h.Restore(saved)
	if err != nil || before.Conversation.Failures != 1 || before.ModelUsage.ReportedRequests != 1 || before.ModelCalls[0].Reservation {
		t.Fatal("received usage or original range lost on interruption", before, err)
	}
	cursor := before.Conversation.Cursor
	model.onCompact = nil
	run, err := f.h.Drive(t.Context(), interrupted.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatal("restart did not recover", run.Status, err)
	}
	after, err := f.h.Restore(run)
	if err != nil || !after.Conversation.Ready || after.Conversation.Failures != 0 || after.Conversation.Cursor.AfterSequence < cursor.AfterSequence || after.ModelUsage.ReportedRequests < 2 {
		t.Fatal("recovered checkpoint inconsistent", after, err)
	}
	ids := map[string]bool{}
	for _, entry := range after.Conversation.Recent {
		if ids[entry.ID] {
			t.Fatal("original range imported twice", entry.ID)
		}
		ids[entry.ID] = true
	}
}

func TestDurableConversationBoundsInvalidSummaryRetries(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	model := durableTestModel(f)
	c, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	model.onCompact = func(context.Context, agentcontract.ConversationCompactionRequest) error {
		return agentcontract.NewHostError("conversation_compaction_invalid")
	}
	for i := range 12 {
		message, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, fmt.Sprint(i), strings.Repeat("保留未压缩原文。", 900), "")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
			t.Fatal(err)
		}
		run, err := f.h.Drive(t.Context(), message.RunID, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == "failed" {
			state, err := f.h.Restore(run)
			if err != nil || state.ErrorCode == nil || *state.ErrorCode != "conversation_compaction_failed" || model.compactions != 3 || state.Conversation.Compactions != 0 || len(state.Conversation.Recent) == 0 || state.ModelUsage.ReportedRequests != 3 {
				t.Fatal("failed compaction dropped data or exceeded retries", state, err)
			}
			return
		}
		if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("did not exercise invalid summary handling")
}
