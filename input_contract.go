package agenstra

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// RequestedInputSchema constrains a single text answer without changing the
// existing SupplyInput(field, text, revision) API.
type RequestedInputSchema struct {
	Type      string   `json:"type"`
	Enum      []string `json:"enum,omitempty"`
	MinLength int      `json:"min_length,omitempty"`
	MaxLength int      `json:"max_length,omitempty"`
}

func (s *RequestedInputSchema) UnmarshalJSON(raw []byte) error {
	type shape RequestedInputSchema
	var parsed shape
	if err := strictUnmarshal(raw, &parsed); err != nil {
		return err
	}
	*s = RequestedInputSchema(parsed)
	return nil
}

func (s RequestedInputSchema) Validate() error {
	switch s.Type {
	case "string":
		if len(s.Enum) != 0 || s.MinLength < 0 || s.MaxLength < 0 || s.MinLength > 30000 || s.MaxLength > 30000 || (s.MaxLength > 0 && s.MinLength > s.MaxLength) {
			return fmt.Errorf("input schema invalid")
		}
	case "enum":
		if len(s.Enum) < 1 || len(s.Enum) > 50 || s.MinLength != 0 || s.MaxLength != 0 {
			return fmt.Errorf("input schema invalid")
		}
		seen := map[string]bool{}
		for _, option := range s.Enum {
			if option == "" || strings.TrimSpace(option) != option || utf8.RuneCountInString(option) > 200 || seen[option] {
				return fmt.Errorf("input schema invalid")
			}
			seen[option] = true
		}
	case "date":
		if len(s.Enum) != 0 || s.MinLength != 0 || s.MaxLength != 0 {
			return fmt.Errorf("input schema invalid")
		}
	default:
		return fmt.Errorf("input schema invalid")
	}
	return nil
}

func ValidateRequestedInput(schema *RequestedInputSchema, text string) error {
	if schema == nil {
		return nil
	}
	if schema.Validate() != nil {
		return hostError("run_state_invalid")
	}
	switch schema.Type {
	case "string":
		length := utf8.RuneCountInString(text)
		if length < schema.MinLength || (schema.MaxLength > 0 && length > schema.MaxLength) {
			return hostError("input_invalid")
		}
	case "enum":
		for _, option := range schema.Enum {
			if text == option {
				return nil
			}
		}
		return hostError("input_invalid")
	case "date":
		parsed, err := time.Parse("2006-01-02", text)
		if err != nil || parsed.Format("2006-01-02") != text {
			return hostError("input_invalid")
		}
	}
	return nil
}
