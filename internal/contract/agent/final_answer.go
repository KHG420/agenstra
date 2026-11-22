package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// FinalAnswerCorrection marks the current model request as a correction of a rejected final payload.
const FinalAnswerCorrection = "final_answer_correction"

// FinalAnswerSchema is a trusted pack's immutable contract for the JSON encoded
// in final.answer_markdown. The prompt and validator describe the same schema.
type FinalAnswerSchema struct {
	source   string
	compiled *jsonschema.Schema
}

// CompileFinalAnswerSchema validates and snapshots the host-supplied local schema.
func CompileFinalAnswerSchema(schema JSON) (*FinalAnswerSchema, error) {
	raw, err := json.Marshal(schema)
	if err != nil || schema == nil || len(raw) > 16<<10 {
		return nil, errors.New("final answer schema must be a local JSON Schema object within 16 KiB")
	}
	compiled, err := ValidateLocalSchema(schema, true)
	if err != nil {
		return nil, err
	}
	return &FinalAnswerSchema{source: string(raw), compiled: compiled}, nil
}

// Prompt describes the final answer format to the model within the run context budget.
func (s *FinalAnswerSchema) Prompt() string {
	return "\nFor kind=final, encode one JSON value matching this schema inside the answer_markdown string. Keep citations and other decision metadata in the surrounding decision. Final answer schema: " + s.source
}

// Normalize leaves field names and values intact. Invalid output is returned
// to the decision loop before a proposal is reviewed, recorded or published.
func (s *FinalAnswerSchema) Normalize(answer string) (string, error) {
	raw := bytes.TrimSpace([]byte(answer))
	if !utf8.Valid(raw) {
		return "", errors.New("answer_markdown must contain valid UTF-8")
	}
	if !json.Valid(raw) {
		if fixed, ok := jsonvalue.RecoverModelContainer(raw); ok {
			raw = fixed
		} else {
			var value json.RawMessage
			return "", json.Unmarshal(raw, &value)
		}
	}
	if jsonvalue.DuplicateKeys(raw) {
		return "", errors.New("each JSON key in answer_markdown must occur only once")
	}
	var value any
	if err := jsonvalue.DecodeStrict(raw, &value); err != nil {
		return "", err
	}
	if err := ValidateSchema(s.compiled, value); err != nil {
		return "", err
	}
	return string(raw), nil
}
