package capability

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"
)

// validRESTResponseJSON rejects invalid Unicode and ambiguous properties before
// decoding can replace characters or overwrite a business status or result.
// Unknown fields and non-object roots keep their response-contract behavior.
func validRESTResponseJSON(raw []byte) bool {
	if !utf8.Valid(raw) || !json.Valid(raw) || !validRESTResponseSurrogates(raw) {
		return false
	}
	type objectKeys struct {
		seen       map[string]bool
		expectsKey bool
	}
	var stack []objectKeys
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		if len(stack) > 0 {
			current := &stack[len(stack)-1]
			if key, ok := token.(string); ok && current.expectsKey {
				if current.seen[key] {
					return false
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

// The caller has checked json.Valid, so escapes occur only inside strings and
// every \u escape has four hexadecimal digits. Skip other escaped characters,
// including literal backslashes, without interpreting their following text.
func validRESTResponseSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		unit, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if unit >= 0xdc00 && unit <= 0xdfff {
			return false
		}
		if unit < 0xd800 || unit > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
