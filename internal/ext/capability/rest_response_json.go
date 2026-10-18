package capability

import (
	"bytes"
	"encoding/json"
	"io"
)

// validRESTResponseJSON rejects ambiguous properties before decoding can replace
// a business status or result with the last duplicate value. Unknown fields and
// non-object roots keep their existing response-contract behavior.
func validRESTResponseJSON(raw []byte) bool {
	if !json.Valid(raw) {
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
