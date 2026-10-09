package agent

import (
	"regexp"
)

// RegistryID validates stable managed package and profile identifiers.
var RegistryID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

// RegistryError carries a safe capability-management error code.
type RegistryError struct{ Code string }

// Error returns the safe error identifier.
func (e *RegistryError) Error() string { return e.Code }

// NewRegistryError constructs a safe capability-management error.
func NewRegistryError(code string) error { return &RegistryError{Code: code} }

// CapabilityDraft shares the registry but never takes part in runtime resolution.
type CapabilityDraft struct {
	DraftID   string            `json:"draft_id"`
	Revision  int               `json:"revision"`
	Manifest  map[string]any    `json:"manifest"`
	Skills    map[string]string `json:"skills"`
	UpdatedAt float64           `json:"updated_at"`
	Issues    []DraftIssue      `json:"issues"`
}

// DraftIssue identifies a validation problem at a specific manifest field.
type DraftIssue struct {
	Section string `json:"section"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// DraftEdit describes one revision-checked edit to a capability draft.
type DraftEdit struct {
	ExpectedRevision *int              `json:"expected_revision"`
	Section          string            `json:"section"`
	Value            map[string]any    `json:"value"`
	Items            []any             `json:"items"`
	Skills           map[string]string `json:"skills"`
	Conflict         string            `json:"conflict"`
	Name             string            `json:"name"`
}
