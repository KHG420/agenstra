package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func (w *WebIntegration) conversationPack(pack string) bool {
	if !w.Config.Chat || !w.Config.ConversationContext {
		return false
	}
	for id, conf := range w.Config.Integrations {
		if id == pack || conf.PackID == pack {
			return true
		}
	}
	return false
}

func (w *WebIntegration) conversationRun(ctx context.Context, run agentcontract.StoredRun) (agentcontract.ChatMessage, int64, error) {
	var m agentcontract.ChatMessage
	var sequence int64
	var raw string
	err := w.Store.store.DB.QueryRowContext(ctx, "SELECT sequence,payload FROM web_messages WHERE owner=$1 AND payload->>'run_id'=$2 LIMIT 1", run.OwnerID, run.RunID).Scan(&sequence, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return m, 0, agentcontract.NewHostError("conversation_required")
	}
	if err == nil {
		err = agentcontract.DecodeDocument(raw, &m)
	}
	return m, sequence, err
}

// ConversationSeed reuses the immediately preceding checkpoint. A legacy chat
// starts at its first original range and is imported incrementally under budget.
func (w *WebIntegration) ConversationSeed(ctx context.Context, run agentcontract.StoredRun) (*agentcontract.ConversationContext, error) {
	release, _ := run.State["pack_release"].(string)
	if !strings.HasPrefix(release, "web:") {
		return nil, nil
	}
	var raw string
	if err := w.Store.store.DB.QueryRowContext(ctx, "SELECT payload FROM web_releases WHERE id=$1", release).Scan(&raw); err != nil {
		return nil, err
	}
	var r webRelease
	if err := agentcontract.DecodeDocument(raw, &r); err != nil {
		return nil, err
	}
	if !r.ConversationContext {
		return nil, nil
	}
	m, sequence, err := w.conversationRun(ctx, run)
	if agentcontract.ErrorCode(err) == "conversation_required" {
		return nil, nil // Non-chat runs may use the same versioned provider.
	}
	if err != nil {
		return nil, err
	}
	seed := &agentcontract.ConversationContext{ConversationID: m.ConversationID, MessageID: m.ID, Recent: []agentcontract.ConversationEntry{}, Requirements: []agentcontract.ConversationRequirement{}}
	var previous string
	err = w.Store.store.DB.QueryRowContext(ctx, "SELECT payload->>'run_id' FROM web_messages WHERE owner=$1 AND conversation=$2 AND sequence<$3 ORDER BY sequence DESC LIMIT 1", run.OwnerID, m.ConversationID, sequence).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return seed, nil
	}
	if err != nil {
		return nil, err
	}
	prior, err := w.Host.Store.GetRun(previous, run.OwnerID)
	if errors.Is(err, runstore.ErrRunNotFound) {
		return seed, nil
	}
	if err != nil {
		return nil, err
	}
	runtime, _ := prior.State["runtime"].(map[string]any)
	if runtime["conversation"] == nil {
		return seed, nil
	}
	data, err := agentcontract.CanonicalJSON(runtime["conversation"])
	if err != nil {
		return nil, err
	}
	if err = jsonvalue.DecodeStrict(data, seed); err != nil {
		return nil, agentcontract.NewHostError("run_state_invalid")
	}
	if seed.ConversationID != m.ConversationID || seed.Cursor.AfterSequence >= sequence {
		return nil, agentcontract.NewHostError("conversation_cursor_invalid")
	}
	seed.MessageID, seed.Ready, seed.Failures = m.ID, false, 0
	return seed, nil
}

// ConversationPage reads one exact original field range before this run's input.
// Owner and upper sequence are derived from the persisted run binding.
func (w *WebIntegration) ConversationPage(ctx context.Context, run agentcontract.StoredRun, cursor agentcontract.ConversationCursor, limit int) (agentcontract.ConversationPage, error) {
	m, upper, err := w.conversationRun(ctx, run)
	if err != nil {
		return agentcontract.ConversationPage{}, err
	}
	if cursor.AfterSequence < 0 || cursor.AfterSequence >= upper || cursor.Part < 0 || cursor.Offset < 0 || limit < 1 || limit > 12000 {
		return agentcontract.ConversationPage{}, agentcontract.NewHostError("conversation_cursor_invalid")
	}
	for {
		var sequence int64
		var raw string
		err = w.Store.store.DB.QueryRowContext(ctx, "SELECT sequence,payload FROM web_messages WHERE owner=$1 AND conversation=$2 AND sequence>$3 AND sequence<$4 ORDER BY sequence LIMIT 1", run.OwnerID, m.ConversationID, cursor.AfterSequence, upper).Scan(&sequence, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return agentcontract.ConversationPage{Next: cursor}, nil
		}
		if err != nil {
			return agentcontract.ConversationPage{}, err
		}
		var old agentcontract.ChatMessage
		if err = agentcontract.DecodeDocument(raw, &old); err != nil {
			return agentcontract.ConversationPage{}, err
		}
		parts, err := w.conversationParts(ctx, run.OwnerID, old)
		if err != nil {
			return agentcontract.ConversationPage{}, err
		}
		if cursor.Part >= len(parts) {
			return agentcontract.ConversationPage{}, agentcontract.NewHostError("conversation_cursor_invalid")
		}
		for cursor.Part < len(parts) {
			part := parts[cursor.Part]
			text := []rune(part.Text)
			if cursor.Offset > len(text) {
				return agentcontract.ConversationPage{}, agentcontract.NewHostError("conversation_cursor_invalid")
			}
			if cursor.Offset < len(text) {
				part.ID = fmt.Sprintf("%s:%d:%d", old.ID, cursor.Part, cursor.Offset)
				part.MessageID, part.Sequence, part.Offset, part.Status = old.ID, sequence, cursor.Offset, old.Status
				end := min(len(text), cursor.Offset+limit)
				part.Text = string(text[cursor.Offset:end])
				cursor.Offset = end
				if end == len(text) {
					cursor.Part++
					cursor.Offset = 0
				}
				if cursor.Part == len(parts) {
					cursor = agentcontract.ConversationCursor{AfterSequence: sequence}
				}
				return agentcontract.ConversationPage{Entry: &part, Next: cursor}, nil
			}
			cursor.Part++
			cursor.Offset = 0
		}
		cursor = agentcontract.ConversationCursor{AfterSequence: sequence}
	}
}

func (w *WebIntegration) conversationParts(ctx context.Context, owner string, m agentcontract.ChatMessage) ([]agentcontract.ConversationEntry, error) {
	parts := []agentcontract.ConversationEntry{{Role: "user", Field: "text", Text: m.Text}}
	var raw sql.NullString
	field := "(state_json->'runtime')->>'followups'"
	if w.Host.Store.Postgres {
		field = "(state_json::jsonb->'runtime')->>'followups'"
	}
	err := w.Host.Store.DB.QueryRowContext(ctx, "SELECT "+field+" FROM runs WHERE owner_id=$1 AND run_id=$2", owner, m.RunID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if raw.Valid {
		var followups []string
		if err = agentcontract.DecodeDocument(raw.String, &followups); err != nil {
			return nil, err
		}
		for i, text := range followups {
			parts = append(parts, agentcontract.ConversationEntry{Role: "user", Field: fmt.Sprintf("followups/%d", i), Text: text})
		}
	}
	if len(m.Attachments) > 0 {
		parts = append(parts, agentcontract.ConversationEntry{Role: "reference", Field: "attachments", Text: attachmentReferences(m.Attachments)})
	}
	parts = append(parts, agentcontract.ConversationEntry{Role: "assistant", Field: "answer_markdown", Text: m.AnswerMarkdown})
	return parts, nil
}

func conversationSnippet(entry agentcontract.ConversationEntry, query string) agentcontract.ConversationEntry {
	lower := strings.ToLower(entry.Text)
	index := strings.Index(lower, strings.ToLower(query))
	if index < 0 {
		return entry
	}
	start := max(0, utf8.RuneCountInString(lower[:index])-160)
	text := []rune(entry.Text)
	entry.Text = string(text[start:min(len(text), start+600)])
	entry.Offset += start
	if end := strings.LastIndex(entry.ID, ":"); end >= 0 {
		entry.ID = fmt.Sprintf("%s:%d", entry.ID[:end], entry.Offset)
	}
	return entry
}
