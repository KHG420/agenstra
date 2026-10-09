package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

// RequestedInputSchema constrains a single text answer without changing the
// existing SupplyInput(field, text, revision) API.
type RequestedInputSchema struct {
	Type      string   `json:"type"`
	Enum      []string `json:"enum,omitempty"`
	MinLength int      `json:"min_length,omitempty"`
	MaxLength int      `json:"max_length,omitempty"`
}

// UnmarshalJSON strictly decodes the schema for a single requested text answer.
func (s *RequestedInputSchema) UnmarshalJSON(raw []byte) error {
	type shape RequestedInputSchema
	var parsed shape
	if err := jsonvalue.DecodeStrict(raw, &parsed); err != nil {
		return err
	}
	*s = RequestedInputSchema(parsed)
	return nil
}

// Validate checks supported answer types and bounds.
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
