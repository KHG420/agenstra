package wechat

import (
	"errors"
	"unicode/utf8"
)

// TextParts splits at UTF-8 boundaries into messages of at most 2,000 bytes.
// It accepts up to 64 KiB and retains the original string without copying it.
func TextParts(text string) ([]string, error) {
	if len(text) > 64<<10 || !utf8.ValidString(text) {
		return nil, errors.New("wechat_text_invalid")
	}
	parts := []string{}
	for len(text) > 0 {
		end := min(len(text), 2000)
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
	return parts, nil
}
