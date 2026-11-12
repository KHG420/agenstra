package agenstra_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"unicode/utf8"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

type wechatTransportFunc func(*http.Request) (*http.Response, error)

func (f wechatTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func wechatJSON(value string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(value))}
}

func TestWeChatPluginQRLoginPollingAndCallerOwnership(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Transport: wechatTransportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Cookie") != "" {
			t.Error("host cookies entered the channel")
		}
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			return wechatJSON(`{"ret":0,"qrcode":"qr-id","qrcode_img_content":"https://example.com/qr"}`), nil
		case "/ilink/bot/get_qrcode_status":
			return wechatJSON(`{"ret":0,"status":"confirmed","bot_token":"private-token","ilink_bot_id":"bot-a","ilink_user_id":"peer-a","baseurl":"https://ilinkai.weixin.qq.com"}`), nil
		case "/ilink/bot/getupdates":
			if r.Header.Get("Authorization") != "Bearer private-token" {
				t.Error("saved channel credentials lost")
			}
			return wechatJSON(`{"ret":0,"get_updates_buf":"next-cursor","msgs":[{"message_id":9223372036854775807,"from_user_id":"peer-a","to_user_id":"bot-a","context_token":"reply-context","item_list":[{"type":1,"text_item":{"text":"hello"}}]}]}`), nil
		}
		t.Error("unexpected channel request", r.URL.Path)
		return wechatJSON(`{"ret":-1}`), nil
	})}
	plugin := agenstra.NewWeChatPlugin(client)
	defer plugin.Close()
	credentials, err := plugin.QRCode(t.Context())
	if err != nil || credentials.QR != "qr-id" {
		t.Fatal("QR login failed", err)
	}
	login, err := plugin.LoginStatus(t.Context(), credentials, "")
	if err != nil || login.Status != "confirmed" || login.PeerID != "peer-a" {
		t.Fatal("login confirmation failed", login, err)
	}
	credentials.Token, credentials.PeerID = login.Token, login.PeerID
	updates, err := plugin.PollUpdates(t.Context(), credentials)
	if err != nil || updates.Cursor != "next-cursor" || len(updates.Messages) != 1 || updates.Messages[0].MessageID.String() != "9223372036854775807" || updates.Messages[0].Items[0].Text.Text != "hello" {
		t.Fatal("polling corrupted the platform message", updates, err)
	}
	if client.Timeout != 0 || client.Jar != jar || client.CheckRedirect != nil {
		t.Fatal("plugin modified the caller HTTP client")
	}
	for _, target := range []string{"http://ilinkai.weixin.qq.com", "https://evil.example", "https://ilinkai.weixin.qq.com:443"} {
		if _, err := agenstra.ValidateWeChatBaseURL(target); err == nil {
			t.Fatal("untrusted credential target accepted", target)
		}
	}
	if svg, err := agenstra.WeChatQRCodeSVG(credentials.QRContent); err != nil || !strings.HasPrefix(svg, "<svg") {
		t.Fatal("QR renderer unavailable", err)
	}
}

func TestWeChatFilePluginEncryptsAndDeliversNativeAttachment(t *testing.T) {
	file, err := agenstra.NewWeChatFile("analysis.pdf", []byte("%PDF-1.7\nexample"))
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(file.Data)
	var uploads, sends int
	client := &http.Client{Transport: wechatTransportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			var fields map[string]any
			if err := json.NewDecoder(r.Body).Decode(&fields); err != nil || fields["media_type"] != float64(3) || fields["rawsize"] != float64(len(original)) {
				t.Error("file upload contract invalid", err)
			}
			return wechatJSON(`{"ret":0,"upload_param":"upload-secret"}`), nil
		case "/c2c/upload":
			if r.URL.Host != "novac2c.cdn.weixin.qq.com" || r.Header.Get("Authorization") != "" || r.URL.Query().Get("filekey") != file.FileKey {
				t.Error("CDN credential boundary invalid")
			}
			ciphertext, err := io.ReadAll(r.Body)
			if err != nil || len(ciphertext)%aes.BlockSize != 0 {
				t.Fatal("invalid file encryption", err)
			}
			key, err := hex.DecodeString(file.Key)
			if err != nil {
				t.Fatal(err)
			}
			block, err := aes.NewCipher(key)
			if err != nil {
				t.Fatal(err)
			}
			for offset := 0; offset < len(ciphertext); offset += aes.BlockSize {
				block.Decrypt(ciphertext[offset:offset+aes.BlockSize], ciphertext[offset:offset+aes.BlockSize])
			}
			padding := int(ciphertext[len(ciphertext)-1])
			if padding < 1 || padding > 16 || !bytes.Equal(ciphertext[len(ciphertext)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) || !bytes.Equal(ciphertext[:len(ciphertext)-padding], original) {
				t.Fatal("file failed independent decryption")
			}
			uploads++
			response := wechatJSON("")
			response.Header.Set("x-encrypted-param", "download-reference")
			return response, nil
		case "/ilink/bot/sendmessage":
			var body struct {
				Message struct {
					ID    string           `json:"client_id"`
					Peer  string           `json:"to_user_id"`
					Items []map[string]any `json:"item_list"`
				} `json:"msg"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message.ID != "stable-reply-id" || body.Message.Peer != "peer-a" || len(body.Message.Items) != 1 {
				t.Fatal("file reply identity invalid", err)
			}
			item := body.Message.Items[0]
			metadata := item["file_item"].(map[string]any)
			media := metadata["media"].(map[string]any)
			if item["type"] != float64(4) || metadata["file_name"] != "analysis.pdf" || media["encrypt_query_param"] != "download-reference" || media["aes_key"] != base64.StdEncoding.EncodeToString([]byte(file.Key)) {
				t.Fatal("native file reference invalid")
			}
			sends++
			return wechatJSON(`{"ret":0}`), nil
		}
		t.Fatal("unexpected file request", r.URL.Path)
		return nil, errors.New("unexpected request")
	})}
	plugin := agenstra.NewWeChatPlugin(client)
	defer plugin.Close()
	credentials := agenstra.WeChatCredentials{BaseURL: "https://ilinkai.weixin.qq.com", Token: "private-token", PeerID: "peer-a"}
	uploaded, err := plugin.UploadFile(t.Context(), credentials, file)
	if err != nil || uploads != 1 || sends != 0 || len(uploaded.Data) != 0 || uploaded.Size != len(original) || !bytes.Equal(file.Data, original) {
		t.Fatal("upload changed the input or delivered prematurely", err)
	}
	if err := plugin.SendFile(t.Context(), credentials, "stable-reply-id", "reply-context", uploaded); err != nil || sends != 1 {
		t.Fatal("file delivery failed", err)
	}
}

func TestWeChatPluginPreservesRejectionsAndBlocksRedirects(t *testing.T) {
	var requests int
	client := &http.Client{Transport: wechatTransportFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return wechatJSON(`{"ret":-2,"errcode":12}`), nil
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://evil.example/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	plugin := agenstra.NewWeChatPlugin(client)
	defer plugin.Close()
	credentials := agenstra.WeChatCredentials{BaseURL: "https://ilinkai.weixin.qq.com", PeerID: "peer", Token: "private-token"}
	err := plugin.SendText(t.Context(), credentials, "reply-id", "context", "hello")
	var rejected *agenstra.WeChatSendError
	if !errors.As(err, &rejected) || rejected.Ret != -2 || rejected.Code != 12 || requests != 1 {
		t.Fatal("platform rejection lost or retried", err)
	}
	if err := plugin.SendText(t.Context(), credentials, "reply-id-2", "context", "hello"); err == nil || requests != 2 {
		t.Fatal("redirect was followed", requests, err)
	}
}

func TestWeChatTextPartsPreserveUTF8AndRejectOversizedInput(t *testing.T) {
	text := strings.Repeat("船π🚢", 1000)
	parts, err := agenstra.WeChatTextParts(text)
	if err != nil || strings.Join(parts, "") != text {
		t.Fatal("reply changed during segmentation", err)
	}
	for _, part := range parts {
		if len(part) > 2000 || !utf8.ValidString(part) {
			t.Fatal("oversized platform message")
		}
	}
	if _, err := agenstra.WeChatTextParts(strings.Repeat("x", (64<<10)+1)); err == nil {
		t.Fatal("unbounded text accepted")
	}
}

func TestWeChatBindingSeparatesPeersAndRetainsMessageIdentity(t *testing.T) {
	credentials := agenstra.WeChatCredentials{BotID: "bot-a", PeerID: "peer-a"}
	message := agenstra.WeChatMessage{MessageID: "12345678901234567890", From: "peer-a", To: "bot-a", Type: 1, State: 2}
	key := agenstra.WeChatMessageKey(credentials, message)
	if !credentials.Accepts(message) || key == "" || key != agenstra.WeChatMessageKey(credentials, message) {
		t.Fatal("bound message lost its stable identity")
	}
	foreign := credentials
	foreign.PeerID = "peer-b"
	if foreign.Accepts(message) || key == agenstra.WeChatMessageKey(foreign, message) {
		t.Fatal("channel binding crossed peers")
	}
	message.GroupID = "group-a"
	if credentials.Accepts(message) {
		t.Fatal("group input entered a direct conversation")
	}
	message.GroupID, message.State = "", 1
	if credentials.Accepts(message) {
		t.Fatal("an incomplete input entered the host")
	}
}

func TestWeChatPluginPropagatesPollCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &http.Client{Transport: wechatTransportFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, r.Context().Err()
	})}
	plugin := agenstra.NewWeChatPlugin(client)
	defer plugin.Close()
	_, err := plugin.PollUpdates(ctx, agenstra.WeChatCredentials{BaseURL: agenstra.WeChatOrigin, Token: "private-token"})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("poll cancellation was hidden", err)
	}
}
