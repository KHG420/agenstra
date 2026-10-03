package jsonvalue

import (
	"encoding/json"
	"testing"
)

func TestDecodeStrictPreservesNumbersAndRejectsIncompleteContracts(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"valid", `{"value":1e6}`, true},
		{"unknown-field", `{"value":1e6,"extra":true}`, false},
		{"second-value", `{"value":1e6} {}`, false},
		{"trailing-garbage", `{"value":1e6} garbage`, false},
		{"incomplete", `{"value":1e6`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value struct {
				Value json.Number `json:"value"`
			}
			err := DecodeStrict([]byte(tc.raw), &value)
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected contract validation: %v", err)
			}
			if tc.valid && value.Value != "1e6" {
				t.Fatalf("JSON number was normalized during decoding: %s", value.Value)
			}
		})
	}
}
