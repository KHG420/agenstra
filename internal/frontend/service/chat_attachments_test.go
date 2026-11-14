package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func attachmentFixture(t *testing.T) (*webFixture, agentcontract.ChatConversation) {
	t.Helper()
	f := newWebFixture(t, &hostModel{}, false)
	limits, err := attachmentLimits(&agentcontract.AttachmentSettings{})
	if err != nil {
		t.Fatal(err)
	}
	f.w.Config.Attachments = &limits
	c, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func TestChatAttachmentsIsolationReferencesAndRetries(t *testing.T) {
	f, c := attachmentFixture(t)
	text := strings.Repeat("文档数据", 3000) + "末尾"
	upload := func() (agentcontract.ChatAttachment, error) {
		return f.w.UploadAttachment(t.Context(), "alice", c.ID, "file-1", "data.txt", strings.NewReader(text))
	}
	file, err := upload()
	if err != nil || !file.PreviewPartial || file.PageCount != 1 {
		t.Fatal(file, err)
	}
	same, err := upload()
	if err != nil || same.ID != file.ID {
		t.Fatal(same, err)
	}
	if _, err := f.w.UploadAttachment(t.Context(), "alice", c.ID, "file-1", "data.txt", strings.NewReader("changed")); agentcontract.ErrorCode(err) != "attachment_conflict" {
		t.Fatal(err)
	}
	if _, err := f.w.ReadAttachment(t.Context(), "bob", c.ID, file.ID, agentcontract.AttachmentRead{}); err == nil {
		t.Fatal("cross-owner read")
	}
	other, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.ReadAttachment(t.Context(), "alice", other.ID, file.ID, agentcontract.AttachmentRead{}); err == nil {
		t.Fatal("cross-conversation read")
	}
	if _, err := f.w.SubmitMessageWithAttachments(t.Context(), "alice", other.ID, "bad", "", "", nil, []string{file.ID}); err == nil {
		t.Fatal("cross-conversation binding")
	}
	message, err := f.w.SubmitMessageWithAttachments(t.Context(), "alice", c.ID, "message", "", "", nil, []string{file.ID})
	if err != nil || len(message.Attachments) != 1 || message.Text != "" {
		t.Fatal(message, err)
	}
	instruction, err := f.w.conversationInstruction("alice", nil, message)
	if err != nil || !strings.Contains(instruction, file.ID) || strings.Contains(instruction, "末尾") {
		t.Fatal("message should carry references and partial preview", err)
	}
	var combined strings.Builder
	offset := 0
	for {
		data, err := f.w.ReadAttachment(t.Context(), "alice", c.ID, file.ID, agentcontract.AttachmentRead{Offset: offset, Limit: 4000})
		if err != nil {
			t.Fatal(err)
		}
		page := data["pages"].([]agentcontract.JSON)[0]
		combined.WriteString(page["text"].(string))
		if page["next_offset"] == nil {
			break
		}
		offset = page["next_offset"].(int)
	}
	if combined.String() != text {
		t.Fatal("incomplete paginated text")
	}
	// The capability uses the durable run-to-conversation association.
	provider, err := f.w.provider(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := provider.Invoke(t.Context(), attachmentReadCapability, map[string]any{"attachment_id": file.ID, "limit": 5}, &agentcontract.InvocationContext{OwnerID: "alice", RunID: message.RunID})
	if err != nil || result.ErrorCode != "" || result.Data["name"] != "data.txt" {
		t.Fatal(result, err)
	}
	if err := f.w.DeleteAttachment(t.Context(), "alice", c.ID, file.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := f.w.SubmitMessageWithAttachments(t.Context(), "alice", c.ID, "message", "", "", nil, []string{file.ID})
	if err != nil || retried.ID != message.ID {
		t.Fatal("lost response retry after delete", err)
	}
	if _, err := f.w.SubmitMessageWithAttachments(t.Context(), "alice", c.ID, "message", "changed", "", nil, []string{file.ID}); agentcontract.ErrorCode(err) != "chat_message_conflict" {
		t.Fatal(err)
	}
}

func TestReadAttachmentFileRequiresPublishedSameConversation(t *testing.T) {
	f, conversation := attachmentFixture(t)
	text := "id,value\nzero,0\nmissing,\n" + strings.Repeat("row,original\n", 6000)
	file, err := f.w.UploadAttachment(t.Context(), "alice", conversation.ID, "raw-file", "data.csv", strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	message, err := f.w.SubmitMessage(t.Context(), "alice", conversation.ID, "before-file", "Import my file", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.w.ReadAttachmentFile(t.Context(), "alice", message.RunID, file.ID); agentcontract.ErrorCode(err) != "attachment_not_found" {
		t.Fatalf("draft file became available to a run: %v", err)
	}
	message, err = f.w.SubmitMessageWithAttachments(t.Context(), "alice", conversation.ID, "published-file", "Import this file", "", nil, []string{file.ID})
	if err != nil {
		t.Fatal(err)
	}
	metadata, original, err := f.w.ReadAttachmentFile(t.Context(), "alice", message.RunID, file.ID)
	if err != nil || metadata.ID != file.ID || string(original) != text {
		t.Fatal(metadata, err)
	}
	original[0] = 'x'
	_, original, err = f.w.ReadAttachmentFile(t.Context(), "alice", message.RunID, file.ID)
	if err != nil || string(original) != text {
		t.Fatal("caller mutated stored bytes", err)
	}
	if _, _, err := f.w.ReadAttachmentFile(t.Context(), "bob", message.RunID, file.ID); err == nil {
		t.Fatal("cross-owner original file read")
	}
	other, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	otherMessage, err := f.w.SubmitMessage(t.Context(), "alice", other.ID, "other", "Read the file", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.w.ReadAttachmentFile(t.Context(), "alice", otherMessage.RunID, file.ID); agentcontract.ErrorCode(err) != "attachment_not_found" {
		t.Fatal("cross-conversation original file read", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := f.w.ReadAttachmentFile(cancelled, "alice", message.RunID, file.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled original file read", err)
	}
}

func TestChatAttachmentCancellationQuotaAndVisionCache(t *testing.T) {
	f, c := attachmentFixture(t)
	started := make(chan struct{})
	f.d.AttachmentParser = func(ctx context.Context, _ agentcontract.AttachmentParseRequest) (agentcontract.AttachmentDocument, error) {
		close(started)
		<-ctx.Done()
		return agentcontract.AttachmentDocument{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := f.w.UploadAttachment(ctx, "alice", c.ID, "cancel", "scan.pdf", strings.NewReader("%PDF-fixture"))
		done <- err
	}()
	<-started
	if _, err := f.w.UploadAttachment(t.Context(), "alice", c.ID, "busy", "scan.pdf", strings.NewReader("%PDF-fixture")); agentcontract.ErrorCode(err) != "attachment_busy" {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var count int
	if err := f.w.Store.store.DB.QueryRow("SELECT COUNT(*) FROM web_attachments").Scan(&count); err != nil || count != 0 {
		t.Fatal("leaked reservation", count, err)
	}
	var calls atomic.Int32
	f.d.AttachmentParser = func(_ context.Context, req agentcontract.AttachmentParseRequest) (agentcontract.AttachmentDocument, error) {
		if req.Page > 0 {
			if req.Page == 1 && req.OriginalText != "封面" {
				t.Error("visual parser lost original page text")
			}
			calls.Add(1)
			return agentcontract.AttachmentDocument{Kind: "pdf", Pages: []agentcontract.AttachmentPage{{Number: req.Page, Text: "图表 123 吨"}}}, nil
		}
		return agentcontract.AttachmentDocument{Kind: "pdf", SupportsVision: true, Pages: []agentcontract.AttachmentPage{{Number: 1, Text: "封面"}, {Number: 2}}}, nil
	}
	file, err := f.w.UploadAttachment(t.Context(), "alice", c.ID, "scan", "scan.pdf", strings.NewReader("%PDF-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		read, err := f.w.ReadAttachment(t.Context(), "alice", c.ID, file.ID, agentcontract.AttachmentRead{Page: 2, Count: 1})
		if err != nil || read["pages"].([]agentcontract.JSON)[0]["text"] != "图表 123 吨" {
			t.Fatal(read, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("visual page not cached")
	}
	if _, err := f.w.ReadAttachment(t.Context(), "alice", c.ID, file.ID, agentcontract.AttachmentRead{Page: 1, Count: 1, Vision: true}); err != nil {
		t.Fatal(err)
	}
	f.w.Config.Attachments.MaxOwnerBytes = maxAttachmentDocumentBytes + 8192 + 12
	if _, err := f.w.UploadAttachment(t.Context(), "alice", c.ID, "excess", "next.txt", strings.NewReader("next")); agentcontract.ErrorCode(err) != "attachment_storage_full" {
		t.Fatal(err)
	}
}

func TestChatAttachmentHTTPUpload(t *testing.T) {
	f, c := attachmentFixture(t)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "table.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(part, "port,price\nA,0"); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/chat/v1/conversations/"+c.ID+"/attachments?client_id=http", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	(&HTTPServer{Web: f.w}).chatHTTP(response, request, "alice")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"page_count":1`) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestDraftAttachmentsWaitForMessagePublication(t *testing.T) {
	f, c := attachmentFixture(t)
	file, err := f.w.UploadAttachment(t.Context(), "alice", c.ID, "draft", "private.txt", strings.NewReader("unsent draft"))
	if err != nil {
		t.Fatal(err)
	}
	message, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, "question", "Hello", "")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := f.w.provider(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	invocation := &agentcontract.InvocationContext{OwnerID: "alice", RunID: message.RunID}
	listed, err := provider.Invoke(t.Context(), attachmentListCapability, map[string]any{}, invocation)
	if err != nil || len(listed.Data["attachments"].([]any)) != 0 {
		t.Fatal("unsent file reached agent", listed, err)
	}
	read, err := provider.Invoke(t.Context(), attachmentReadCapability, map[string]any{"attachment_id": file.ID}, invocation)
	if err != nil || read.ErrorCode != "attachment_not_found" {
		t.Fatal("unsent file readable by agent", read, err)
	}
	files, err := f.w.ListAttachments(t.Context(), "alice", c.ID)
	if err != nil || len(files) != 1 {
		t.Fatal("owner could not manage draft", files, err)
	}
}

func TestAttachmentEvidenceSupportsOriginalJSONPaths(t *testing.T) {
	f, c := attachmentFixture(t)
	file, err := f.w.UploadAttachment(t.Context(), "alice", c.ID, "evidence", "data.txt", strings.NewReader(strings.Repeat("x", 8000)))
	if err != nil {
		t.Fatal(err)
	}
	message, err := f.w.SubmitMessageWithAttachments(t.Context(), "alice", c.ID, "read", "Read this file", "", nil, []string{file.ID})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := f.w.provider(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	call := agentcontract.ToolCall{CallRef: "read-file", Capability: attachmentReadCapability, Arguments: agentcontract.JSON{"attachment_id": file.ID}}
	outcome, err := reactcore.ExecuteCall(t.Context(), provider, map[string]bool{attachmentReadCapability: true}, call, &agentcontract.InvocationContext{OwnerID: "alice", RunID: message.RunID})
	if err != nil || outcome.Fact == nil {
		t.Fatal(outcome, err)
	}
	fact := *outcome.Fact
	value, err := reactcore.ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"data", "pages", 0, "text"}}}, map[string]agentcontract.Fact{fact.FactID: fact}, "", false)
	if err != nil || len(value.(string)) != 6000 {
		t.Fatal("attachment field was not inspectable", err)
	}
}
