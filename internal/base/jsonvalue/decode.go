package jsonvalue

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
)

// DecodeStrict accepts one complete JSON document, rejecting unknown fields.
// JSON numbers retain their textual representation.
func DecodeStrict(raw []byte, v any) error {
	if !json.Valid(raw) {
		return errors.New("invalid JSON document")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	return dec.Decode(v)
}

// Index reads an integer index from a Go value or a decoded JSON number.
func Index(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case json.Number:
		i, e := x.Int64()
		return int(i), e == nil
	case float64:
		if x >= 0 && x == math.Trunc(x) {
			return int(x), true
		}
	}
	return 0, false
}
