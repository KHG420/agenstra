package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// ReadAttachmentFile returns caller-owned original bytes for a host file adapter.
// The file must be published in the run's own conversation. Current attachment
// authorization, model-data policy and the configured file limit still apply.
// Hosts should pass these bytes to their authorized service, outside model input.
func (w *WebIntegration) ReadAttachmentFile(ctx context.Context, owner, run, id string) (agentcontract.ChatAttachment, []byte, error) {
	var metadata agentcontract.ChatAttachment
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case w.attachmentSlots <- struct{}{}:
		defer func() { <-w.attachmentSlots }()
	default:
		return metadata, nil, agentcontract.NewHostError("attachment_busy")
	}
	var conversation string
	err := w.Store.store.DB.QueryRowContext(ctx, "SELECT conversation FROM web_messages WHERE owner=$1 AND payload->>'run_id'=$2 LIMIT 1", owner, run).Scan(&conversation)
	if errors.Is(err, sql.ErrNoRows) {
		return metadata, nil, agentcontract.NewHostError("attachment_conversation_required")
	}
	if err != nil {
		return metadata, nil, err
	}
	if _, err := w.attachmentConversation(ctx, owner, conversation, true); err != nil {
		return metadata, nil, err
	}
	var raw, checksum string
	var data []byte
	err = w.Store.store.DB.QueryRowContext(ctx, "SELECT payload,data,digest FROM web_attachments WHERE id=$1 AND owner=$2 AND conversation=$3 AND status='ready' AND bound=1 AND length(data)<=$4", id, owner, conversation, w.Config.Attachments.MaxFileBytes).Scan(&raw, &data, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return metadata, nil, agentcontract.NewHostError("attachment_not_found")
	}
	if err != nil {
		return metadata, nil, err
	}
	if err := agentcontract.DecodeDocument(raw, &metadata); err != nil {
		return metadata, nil, err
	}
	digest := sha256.Sum256(data)
	if metadata.ID != id || metadata.ConversationID != conversation || metadata.Size != int64(len(data)) || metadata.Size < 1 || metadata.Size > w.Config.Attachments.MaxFileBytes || checksum != hex.EncodeToString(digest[:]) {
		return metadata, nil, agentcontract.NewHostError("attachment_document_invalid")
	}
	if _, err := w.attachmentConversation(ctx, owner, conversation, true); err != nil {
		return metadata, nil, err
	}
	return metadata, data, nil
}
