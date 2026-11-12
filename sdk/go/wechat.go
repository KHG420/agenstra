package agenstra

import (
	"net/http"

	"github.com/KHG420/agenstra/internal/platform/wechat"
)

// WeChatOrigin is the default iLink HTTPS API origin used for new logins.
const WeChatOrigin = wechat.Origin

// WeChatVersion is the channel protocol version sent by the plugin.
const WeChatVersion = wechat.Version

// WeChatPlugin provides the iLink channel protocol for trusted host integrations.
// Configure its client before use and close it after requests and polls stop.
type WeChatPlugin = wechat.Plugin

// WeChatCredentials binds a bot token, peer and poll cursor in trusted host storage.
type WeChatCredentials = wechat.Credentials

// WeChatLogin contains the platform QR confirmation status and returned credentials.
type WeChatLogin = wechat.Login

// WeChatID preserves platform string and numeric message identities exactly.
type WeChatID = wechat.ID

// WeChatItem contains the incoming text and platform-transcribed voice fields.
type WeChatItem = wechat.Item

// WeChatMessage contains one peer message and its original reply context.
type WeChatMessage = wechat.Incoming

// WeChatUpdates contains the platform messages and the next durable poll cursor.
type WeChatUpdates = wechat.Updates

// WeChatSendError identifies an explicit platform rejection by ret and errcode.
// Other transport errors leave the remote message outcome uncertain.
type WeChatSendError = wechat.SendError

// WeChatFile contains one upload identity or an uploaded file reference.
// Its key and any retained bytes require trusted encrypted host storage.
type WeChatFile = wechat.File

// NewWeChatPlugin creates a reusable channel plugin with finite requests and no redirects.
// A supplied HTTP client remains caller-owned and is never modified.
func NewWeChatPlugin(client *http.Client) *WeChatPlugin { return wechat.New(client) }

// NewWeChatFile copies a file up to 8 MiB and creates its stable upload identity.
// Persist the result before uploading and reuse it for a bounded retry.
func NewWeChatFile(name string, data []byte) (WeChatFile, error) {
	return wechat.NewFile(name, data)
}

// ValidateWeChatBaseURL accepts the platform HTTPS API origins used for credentials.
func ValidateWeChatBaseURL(raw string) (string, error) { return wechat.ValidateBaseURL(raw) }

// WeChatQRCodeSVG renders a bounded platform login payload as a QR SVG.
func WeChatQRCodeSVG(text string) (string, error) { return wechat.QRCodeSVG(text) }

// WeChatTextParts splits a valid UTF-8 reply up to 64 KiB into 2,000-byte messages.
func WeChatTextParts(text string) ([]string, error) { return wechat.TextParts(text) }

// WeChatMessageKey returns a stable incoming key scoped to the current bot and peer.
func WeChatMessageKey(credentials WeChatCredentials, message WeChatMessage) string {
	return wechat.MessageKey(credentials, message)
}
