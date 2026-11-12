package wechat

import (
	"crypto/sha256"
	"encoding/hex"
)

// Accepts checks that a finished direct input belongs to the bound bot and peer.
// The host resolves that trusted binding to its current account authorization.
func (c Credentials) Accepts(message Incoming) bool {
	return c.PeerID != "" && c.BotID != "" && message.From == c.PeerID &&
		(message.To == "" || message.To == c.BotID) && message.GroupID == "" &&
		message.Type == 1 && (message.State == 0 || message.State == 2)
}

// MessageKey returns an opaque stable key scoped to the bot, peer and incoming ID.
// Persist it with the poll cursor before submitting a framework chat message.
func MessageKey(credentials Credentials, message Incoming) string {
	id := message.MessageID.String()
	if id == "" {
		id = message.ClientID
	}
	if id == "" || len(id) > 256 {
		return ""
	}
	digest := sha256.Sum256([]byte(credentials.BotID + "\x00" + credentials.PeerID + "\x00" + id))
	return hex.EncodeToString(digest[:])
}
