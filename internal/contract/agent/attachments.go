package agent

import "context"

// AttachmentSettings enables owner-scoped files in chat and bounds their storage.
type AttachmentSettings struct {
	MaxFileBytes  int64 `json:"max_file_bytes,omitempty"`
	MaxOwnerBytes int64 `json:"max_owner_bytes,omitempty"`
	MaxTotalBytes int64 `json:"max_total_bytes,omitempty"`
}

// ChatAttachment is a server-owned reference to a file in one conversation.
type ChatAttachment struct {
	ID             string  `json:"id"`
	ConversationID string  `json:"conversation_id"`
	Name           string  `json:"name"`
	MediaType      string  `json:"media_type"`
	Kind           string  `json:"kind"`
	Size           int64   `json:"size"`
	PageCount      int     `json:"page_count"`
	Preview        string  `json:"preview"`
	PreviewPartial bool    `json:"preview_partial"`
	CreatedAt      float64 `json:"created_at"`
}

// AttachmentPage retains the original one-based page number and extracted text.
type AttachmentPage struct {
	Number     int    `json:"number"`
	Text       string `json:"text"`
	VisualText string `json:"visual_text,omitempty"`
}

// AttachmentDocument is the bounded parser result persisted with an upload.
type AttachmentDocument struct {
	Kind           string           `json:"kind"`
	Pages          []AttachmentPage `json:"pages"`
	SupportsVision bool             `json:"supports_vision,omitempty"`
}

// AttachmentParseRequest gives a trusted parser the caller's authorized file.
// Page zero extracts the document. A positive Page requests visual interpretation
// of that original page. Data is read-only and must not be retained by the parser.
type AttachmentParseRequest struct {
	OwnerID        string
	ConversationID string
	AttachmentID   string
	Name           string
	MediaType      string
	Data           []byte
	Page           int
}

// AttachmentParser connects chat uploads to an application-selected parser.
// It must honor cancellation and return source data without following document instructions.
type AttachmentParser func(context.Context, AttachmentParseRequest) (AttachmentDocument, error)

// AttachmentRead selects a bounded range of original pages and text offsets.
type AttachmentRead struct {
	Page   int  `json:"page,omitempty"`
	Count  int  `json:"count,omitempty"`
	Offset int  `json:"offset,omitempty"`
	Limit  int  `json:"limit,omitempty"`
	Vision bool `json:"vision,omitempty"`
}
