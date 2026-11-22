package modelapi

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// Empty tool arguments denote no parameters only when the current declared
// function's schema accepts an empty object. Required fields still fail.
func normalizeModelToolJSON(body []byte, schema agentcontract.JSON) ([]byte, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		empty := []byte("{}")
		if validateNativeDecisionOutput(empty, schema) == "" {
			log.Printf("model JSON normalized: bytes=%d normalized_bytes=2 reason=empty_parameters", len(body))
			return empty, true
		}
		return body, false
	}
	return normalizeModelJSON(body)
}

// Normalize only formatting around a complete object. Every field, scalar and
// numeric representation then passes the original duplicate-key, schema and
// runtime checks. Incomplete values retain the original recovery path.
func normalizeModelJSON(body []byte) ([]byte, bool) {
	if json.Valid(body) {
		return body, false
	}
	candidate := bytes.Trim(body, " \t\r\n")
	reasons := []string{}
	if bytes.HasPrefix(candidate, []byte("\xef\xbb\xbf")) {
		candidate = bytes.TrimSpace(candidate[3:])
		reasons = append(reasons, "utf8_bom")
	}
	if unwrapped := unwrapModelJSON(candidate); len(unwrapped) != len(candidate) {
		candidate = unwrapped
		reasons = append(reasons, "markdown_fence")
	}
	if len(candidate) == 0 || candidate[0] != '{' {
		return body, false
	}
	if fixed, changed := jsonvalue.RepairModelPunctuation(candidate); changed {
		candidate = fixed
		reasons = append(reasons, "json_punctuation")
	}
	if !json.Valid(candidate) {
		decoder := json.NewDecoder(bytes.NewReader(candidate))
		var object json.RawMessage
		if decoder.Decode(&object) != nil {
			return body, false
		}
		end := decoder.InputOffset()
		suffix := candidate[end:]
		if len(suffix) == 0 || len(suffix) > 32 || len(bytes.Trim(suffix, "}] \t\r\n")) != 0 {
			return body, false
		}
		candidate = candidate[:end]
		reasons = append(reasons, "extra_closing_delimiters")
	}
	log.Printf("model JSON normalized: bytes=%d normalized_bytes=%d reason=%s", len(body), len(candidate), strings.Join(reasons, ","))
	return candidate, true
}

// Recover presentation mistakes only for final answers. Incomplete action
// envelopes, multiple alternatives and explicit truncation stay on the retry path.
func recoverFinalOutput(raw []byte, nativeFinal bool) ([]byte, bool) {
	if json.Valid(raw) {
		return raw, false
	}
	fixed, ok := jsonvalue.RecoverModelContainer(raw)
	if !ok {
		return raw, false
	}
	if !nativeFinal {
		var header struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(fixed, &header) != nil || header.Kind != "final" {
			return raw, false
		}
	}
	return fixed, true
}
