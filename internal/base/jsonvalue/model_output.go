package jsonvalue

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RecoverModelContainer isolates one object or array in a model response.
// Only enclosing delimiters can be appended, after a self-terminating value.
// An unfinished scalar, another candidate or conflicting framing is ambiguous.
func RecoverModelContainer(raw []byte) ([]byte, bool) {
	if len(raw) == 0 || len(raw) > 8<<20 || !utf8.Valid(raw) {
		return raw, false
	}
	body := bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(raw), []byte("\xef\xbb\xbf")))
	start := bytes.IndexAny(body, "{[")
	if start < 0 {
		return raw, false
	}
	prefix, body := body[:start], body[start:]
	fence := []byte("```")
	if opening := bytes.Index(prefix, fence); opening >= 0 {
		label, newline, ok := bytes.Cut(prefix[opening+len(fence):], []byte("\n"))
		label = bytes.TrimSpace(label)
		if !ok || len(bytes.TrimSpace(newline)) != 0 || (len(label) != 0 && !bytes.EqualFold(label, []byte("json"))) {
			return raw, false
		}
		end := bytes.LastIndex(body, fence)
		if end < 1 || !proseFrame(body[end+len(fence):]) {
			return raw, false
		}
		before := bytes.TrimRight(body[:end], " \t\r")
		if len(before) == 0 || before[len(before)-1] != '\n' {
			return raw, false
		}
		prefix, body = prefix[:opening], bytes.TrimSpace(before)
	}
	if !proseFrame(prefix) {
		return raw, false
	}
	var closers []byte
	var quoted, escaped bool
	end := len(body)
	for i, b := range body {
		if quoted {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			if len(closers) >= 256 {
				return raw, false
			}
			closeByte := byte('}')
			if b == '[' {
				closeByte = ']'
			}
			closers = append(closers, closeByte)
		case '}', ']':
			if len(closers) == 0 || closers[len(closers)-1] != b {
				return raw, false
			}
			closers = closers[:len(closers)-1]
			if len(closers) == 0 {
				end = i + 1
			}
		}
		if end == i+1 {
			break
		}
	}
	if quoted || !proseFrame(body[end:]) {
		return raw, false
	}
	candidate := bytes.TrimSpace(body[:end])
	if len(closers) > 0 {
		if len(candidate) == 0 || !strings.ContainsRune("\"}]el", rune(candidate[len(candidate)-1])) {
			return raw, false
		}
		candidate = bytes.Clone(candidate)
		for i := len(closers) - 1; i >= 0; i-- {
			candidate = append(candidate, closers[i])
		}
	}
	if !json.Valid(candidate) {
		fixed, ok := RepairModelPunctuation(candidate)
		if !ok || !json.Valid(fixed) {
			return raw, false
		}
		candidate = fixed
	}
	return candidate, !bytes.Equal(raw, candidate)
}

// Explanatory lines may surround one value. A second JSON value, quoted
// example, code fence or tool protocol markup cannot act as an explanation.
func proseFrame(frame []byte) bool {
	frame = bytes.TrimSpace(frame)
	if len(frame) == 0 {
		return true
	}
	if bytes.ContainsAny(frame, "{}[]`\"<>\\") {
		return false
	}
	for line := range strings.Lines(string(frame)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		first, _ := utf8.DecodeRuneInString(line)
		if !unicode.IsLetter(first) {
			return false
		}
		for _, literal := range []string{"true", "false", "null"} {
			if rest, ok := strings.CutPrefix(line, literal); ok {
				next, _ := utf8.DecodeRuneInString(rest)
				if !unicode.IsLetter(next) {
					return false
				}
			}
		}
	}
	return true
}

// DuplicateKeys checks a valid JSON document, including escaped keys in nested objects.
func DuplicateKeys(body []byte) bool {
	type objectKeys struct {
		seen       map[string]bool
		expectsKey bool
	}
	var stack []objectKeys
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
		if len(stack) > 0 {
			current := &stack[len(stack)-1]
			if key, ok := token.(string); ok && current.expectsKey {
				if current.seen[key] {
					return true
				}
				current.seen[key], current.expectsKey = true, false
				continue
			}
			current.expectsKey = current.seen != nil
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				stack = append(stack, objectKeys{seen: map[string]bool{}, expectsKey: true})
			case '[':
				stack = append(stack, objectKeys{})
			case '}', ']':
				stack = stack[:len(stack)-1]
			}
		}
	}
}

// RepairModelPunctuation preserves quoted bytes and escapes. It removes a trailing
// comma only after a value and escapes literal control characters in strings.
// The full JSON parser determines whether the resulting object is complete.
func RepairModelPunctuation(body []byte) ([]byte, bool) {
	var output []byte
	var stack []byte
	redundant := 0
	inside, escaped := false, false
	last := byte(0)
	start := 0
	for i, current := range body {
		if inside {
			if escaped {
				escaped = false
				continue
			}
			switch current {
			case '\\':
				escaped = true
			case '"':
				inside = false
				last = '"'
			default:
				if current < 0x20 {
					if len(output)+i-start+6 > 8<<20 {
						return body, false
					}
					output = append(output, body[start:i]...)
					const hex = "0123456789abcdef"
					output = append(output, '\\', 'u', '0', '0', hex[current>>4], hex[current&15])
					start = i + 1
				}
			}
			continue
		}
		if current == '"' {
			inside = true
		} else if current == '{' || current == '[' {
			stack = append(stack, current)
		} else if (current == '}' || current == ']') && len(stack) > 0 {
			opening := stack[len(stack)-1]
			if opening == '{' && current == '}' || opening == '[' && current == ']' {
				stack = stack[:len(stack)-1]
			} else {
				redundant++
				if redundant > 32 {
					return body, false
				}
				output = append(output, body[start:i]...)
				start = i + 1
				continue
			}
		} else if current == ',' && strings.ContainsRune(`"}]0123456789el`, rune(last)) {
			next := i + 1
			for next < len(body) && strings.ContainsRune(" \t\r\n", rune(body[next])) {
				next++
			}
			if next < len(body) && (body[next] == '}' || body[next] == ']') {
				output = append(output, body[start:i]...)
				start = i + 1
			}
		}
		if !strings.ContainsRune(" \t\r\n", rune(current)) {
			last = current
		}
	}
	if output == nil && start == 0 {
		return body, false
	}
	if len(output)+len(body)-start > 8<<20 {
		return body, false
	}
	return append(output, body[start:]...), true
}
