package service

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// ReadAttachment returns original pages within a bounded text window.
// Empty scanned pages and explicit Vision reads use the configured parser;
// their interpretation is cached against the immutable stored file.
func (w *WebIntegration) ReadAttachment(ctx context.Context, owner, conversation, id string, request agentcontract.AttachmentRead) (agentcontract.JSON, error) {
	if _, err := w.attachmentConversation(ctx, owner, conversation, true); err != nil {
		return nil, err
	}
	select {
	case w.attachmentSlots <- struct{}{}:
		defer func() { <-w.attachmentSlots }()
	default:
		return nil, agentcontract.NewHostError("attachment_busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	if request.Page == 0 {
		request.Page = 1
	}
	if request.Count == 0 {
		request.Count = 4
	}
	if request.Limit == 0 {
		request.Limit = 6000
	}
	if request.Page < 1 || request.Count < 1 || request.Count > 8 || request.Offset < 0 || request.Limit < 1 || request.Limit > 12000 {
		return nil, agentcontract.NewHostError("attachment_page_invalid")
	}
	var raw, content string
	if err := w.Store.store.DB.QueryRowContext(ctx, "SELECT payload,document FROM web_attachments WHERE id=$1 AND owner=$2 AND conversation=$3 AND status='ready'", id, owner, conversation).Scan(&raw, &content); err != nil {
		return nil, agentcontract.NewHostError("attachment_not_found")
	}
	var metadata agentcontract.ChatAttachment
	var document agentcontract.AttachmentDocument
	if err := agentcontract.DecodeDocument(raw, &metadata); err != nil {
		return nil, err
	}
	if err := agentcontract.DecodeDocument(content, &document); err != nil {
		return nil, err
	}
	if request.Page > len(document.Pages) {
		return nil, agentcontract.NewHostError("attachment_page_invalid")
	}
	pages := []agentcontract.JSON{}
	remaining, end := 24000, min(len(document.Pages), request.Page-1+request.Count)
	var nextPage any
	for index := request.Page - 1; index < end; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if remaining == 0 {
			nextPage = index + 1
			break
		}
		page := document.Pages[index]
		visual := request.Vision || strings.TrimSpace(page.Text) == "" && document.SupportsVision
		text := page.Text
		if visual {
			if !document.SupportsVision {
				return nil, agentcontract.NewHostError("attachment_vision_unavailable")
			}
			text = page.VisualText
			if text == "" {
				var err error
				text, err = w.visualAttachmentPage(ctx, owner, conversation, metadata, page.Number, page.Text)
				if err != nil {
					return nil, err
				}
			}
		}
		characters := []rune(text)
		offset := 0
		if index == request.Page-1 {
			offset = request.Offset
		}
		if offset > len(characters) {
			return nil, agentcontract.NewHostError("attachment_page_invalid")
		}
		to := min(len(characters), offset+min(request.Limit, remaining))
		var nextOffset any
		if to < len(characters) {
			nextOffset = to
		}
		pages = append(pages, agentcontract.JSON{"page": page.Number, "text": string(characters[offset:to]), "visual": visual, "next_offset": nextOffset})
		remaining -= to - offset
	}
	if nextPage == nil && end < len(document.Pages) {
		nextPage = end + 1
	}
	if _, err := w.attachmentConversation(ctx, owner, conversation, true); err != nil {
		return nil, err
	}
	return agentcontract.JSON{"attachment_id": id, "name": metadata.Name, "page_count": len(document.Pages), "pages": pages, "next_page": nextPage}, nil
}

func (w *WebIntegration) visualAttachmentPage(ctx context.Context, owner, conversation string, metadata agentcontract.ChatAttachment, page int, originalText string) (string, error) {
	parser := w.deployment.AttachmentParser
	if parser == nil {
		return "", agentcontract.NewHostError("attachment_vision_unavailable")
	}
	var data []byte
	if err := w.Store.store.DB.QueryRowContext(ctx, "SELECT data FROM web_attachments WHERE id=$1 AND owner=$2 AND conversation=$3 AND status='ready'", metadata.ID, owner, conversation).Scan(&data); err != nil {
		return "", agentcontract.NewHostError("attachment_not_found")
	}
	if int64(len(data)) > w.Config.Attachments.MaxFileBytes || int64(len(data)) != metadata.Size {
		return "", agentcontract.NewHostError("attachment_document_invalid")
	}
	parsed, err := parser(ctx, agentcontract.AttachmentParseRequest{OwnerID: owner, ConversationID: conversation, AttachmentID: metadata.ID, Name: metadata.Name, MediaType: metadata.MediaType, Data: data, Page: page, OriginalText: originalText})
	if err != nil {
		return "", err
	}
	if len(parsed.Pages) != 1 || parsed.Pages[0].Number != page || !utf8.ValidString(parsed.Pages[0].Text) || strings.TrimSpace(parsed.Pages[0].Text) == "" || len(parsed.Pages[0].Text) > 64000 {
		return "", agentcontract.NewHostError("attachment_document_invalid")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	text := parsed.Pages[0].Text
	if _, err := w.attachmentConversation(ctx, owner, conversation, true); err != nil {
		return "", err
	}
	err = w.Store.store.Transaction(func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRow("SELECT document FROM web_attachments WHERE id=$1 AND owner=$2 AND conversation=$3 AND status='ready'", metadata.ID, owner, conversation).Scan(&raw); err != nil {
			return agentcontract.NewHostError("attachment_not_found")
		}
		var current agentcontract.AttachmentDocument
		if err := agentcontract.DecodeDocument(raw, &current); err != nil {
			return err
		}
		if page > len(current.Pages) {
			return agentcontract.NewHostError("attachment_page_invalid")
		}
		if current.Pages[page-1].VisualText != "" {
			text = current.Pages[page-1].VisualText
			return nil
		}
		current.Pages[page-1].VisualText = text
		raw, err := validateAttachmentDocument(current, metadata.Kind)
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE web_attachments SET document=$1 WHERE id=$2 AND owner=$3", raw, metadata.ID, owner)
		return err
	})
	return text, err
}
