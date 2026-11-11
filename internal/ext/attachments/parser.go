// Package attachments provides bounded, model-independent attachment parsers.
package attachments

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path/filepath"
	"strings"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// FileType validates the supported file envelope and derives its actual media type.
func FileType(name string, data []byte) (kind, mediaType string, err error) {
	fail := func(code string) (string, string, error) { return "", "", agentcontract.NewHostError(code) }
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".csv", ".tsv", ".json":
		if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
			return fail("attachment_text_encoding")
		}
		return "text", "text/plain; charset=utf-8", nil
	case ".xlsx":
		if !bytes.HasPrefix(data, []byte("PK")) {
			return fail("attachment_spreadsheet_invalid")
		}
		return "spreadsheet", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	case ".pdf":
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return fail("attachment_pdf_invalid")
		}
		return "pdf", "application/pdf", nil
	case ".png", ".jpg", ".jpeg", ".gif":
		config, format, decodeErr := image.DecodeConfig(bytes.NewReader(data))
		if decodeErr != nil || config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 || config.Width*config.Height > 32000000 {
			return fail("attachment_image_invalid")
		}
		return "image", "image/" + format, nil
	}
	return fail("attachment_type_unsupported")
}

// Parse extracts UTF-8 text and saved XLSX cells without executing file content.
// PDF and image interpretation are provided by the configured AttachmentParser.
func Parse(ctx context.Context, request agentcontract.AttachmentParseRequest) (agentcontract.AttachmentDocument, error) {
	if err := ctx.Err(); err != nil {
		return agentcontract.AttachmentDocument{}, err
	}
	kind, _, err := FileType(request.Name, request.Data)
	if err != nil {
		return agentcontract.AttachmentDocument{}, err
	}
	var text string
	switch kind {
	case "text":
		text = strings.TrimPrefix(string(request.Data), "\ufeff")
	case "spreadsheet":
		text, err = xlsxText(ctx, request.Data)
	default:
		err = agentcontract.NewHostError("attachment_parser_unavailable")
	}
	if err != nil {
		return agentcontract.AttachmentDocument{}, agentcontract.NewHostError(err.Error())
	}
	if strings.TrimSpace(text) == "" {
		return agentcontract.AttachmentDocument{}, agentcontract.NewHostError("attachment_empty")
	}
	return agentcontract.AttachmentDocument{Kind: kind, Pages: []agentcontract.AttachmentPage{{Number: 1, Text: text}}}, nil
}
