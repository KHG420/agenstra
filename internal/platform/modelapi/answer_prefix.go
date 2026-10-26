package modelapi

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Decode only a top-level user-facing answer. An unfinished JSON escape or
// surrogate pair stays buffered; tool arguments and reasoning never surface.
func answerPrefix(raw string, requireFinal bool) string {
	i := 0
	space := func() {
		for i < len(raw) && strings.ContainsRune(" \n\r\t", rune(raw[i])) {
			i++
		}
	}
	space()
	if i >= len(raw) || raw[i] != '{' {
		return ""
	}
	i++
	answer, kind := "", ""
	seen := map[string]bool{}
	for {
		space()
		if i >= len(raw) || raw[i] == '}' {
			break
		}
		key, n, complete, ok := stringPrefix(raw[i:])
		if !ok {
			return ""
		}
		if !complete {
			break
		}
		i += n
		if seen[key] {
			return ""
		}
		seen[key] = true
		space()
		if i >= len(raw) {
			break
		}
		if raw[i] != ':' {
			return ""
		}
		i++
		space()
		if i >= len(raw) {
			break
		}
		if raw[i] == '"' {
			value, n, complete, ok := stringPrefix(raw[i:])
			if !ok {
				return ""
			}
			i += n
			switch key {
			case "answer_markdown":
				answer = value
			case "kind":
				if complete {
					kind = value
				}
			case "completion_feedback":
				if value != "" {
					return ""
				}
			}
			if !complete {
				break
			}
		} else {
			decoder := json.NewDecoder(strings.NewReader(raw[i:]))
			var value json.RawMessage
			if decoder.Decode(&value) != nil {
				break
			}
			i += int(decoder.InputOffset())
		}
		space()
		if i >= len(raw) || raw[i] == '}' {
			break
		}
		if raw[i] != ',' {
			return ""
		}
		i++
	}
	if requireFinal && kind != "final" {
		return ""
	}
	return answer
}

func stringPrefix(raw string) (string, int, bool, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", 0, false, false
	}
	var out strings.Builder
	for i := 1; i < len(raw); {
		c := raw[i]
		if c == '"' {
			return out.String(), i + 1, true, true
		}
		if c < 0x20 {
			return "", i, false, false
		}
		if c != '\\' {
			if !utf8.FullRuneInString(raw[i:]) {
				return out.String(), i, false, true
			}
			r, n := utf8.DecodeRuneInString(raw[i:])
			if r == utf8.RuneError && n == 1 {
				return "", i, false, false
			}
			out.WriteRune(r)
			i += n
			continue
		}
		start := i
		i++
		if i >= len(raw) {
			return out.String(), start, false, true
		}
		switch raw[i] {
		case '"', '\\', '/':
			out.WriteByte(raw[i])
			i++
		case 'b':
			out.WriteByte('\b')
			i++
		case 'f':
			out.WriteByte('\f')
			i++
		case 'n':
			out.WriteByte('\n')
			i++
		case 'r':
			out.WriteByte('\r')
			i++
		case 't':
			out.WriteByte('\t')
			i++
		case 'u':
			if i+5 > len(raw) {
				return out.String(), start, false, true
			}
			v, err := strconv.ParseUint(raw[i+1:i+5], 16, 16)
			if err != nil {
				return "", i, false, false
			}
			r := rune(v)
			i += 5
			if r >= 0xd800 && r <= 0xdbff {
				if i+6 > len(raw) {
					return out.String(), start, false, true
				}
				if raw[i:i+2] != "\\u" {
					return "", i, false, false
				}
				v, err = strconv.ParseUint(raw[i+2:i+6], 16, 16)
				if err != nil || v < 0xdc00 || v > 0xdfff {
					return "", i, false, false
				}
				r = utf16.DecodeRune(r, rune(v))
				i += 6
			} else if r >= 0xdc00 && r <= 0xdfff {
				return "", i, false, false
			}
			out.WriteRune(r)
		default:
			return "", i, false, false
		}
	}
	return out.String(), len(raw), false, true
}
