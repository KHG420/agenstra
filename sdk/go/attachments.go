package agenstra

import (
	"context"

	"github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/ext/attachments"
)

// AttachmentSettings enables owner-scoped chat file storage and its byte limits.
type AttachmentSettings = agent.AttachmentSettings

// ChatAttachment is a server-owned file reference in one conversation.
type ChatAttachment = agent.ChatAttachment

// AttachmentPage retains original page numbers and extracted content.
type AttachmentPage = agent.AttachmentPage

// AttachmentDocument is the parser output saved by the framework.
type AttachmentDocument = agent.AttachmentDocument

// AttachmentParseRequest contains the authorized source file and optional visual page.
type AttachmentParseRequest = agent.AttachmentParseRequest

// AttachmentParser connects uploads to the host's selected parsing services.
type AttachmentParser = agent.AttachmentParser

// AttachmentRead selects a bounded page range and text offset.
type AttachmentRead = agent.AttachmentRead

// ParseTextAttachment uses the framework's bounded UTF-8 and XLSX parsers.
func ParseTextAttachment(ctx context.Context, request AttachmentParseRequest) (AttachmentDocument, error) {
	return attachments.Parse(ctx, request)
}
