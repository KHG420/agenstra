package agent

import (
	"crypto/sha256"
	"encoding/hex"
)

// WebIntegrationConfig enables optional chat and browser integration with trusted session settings.
type WebIntegrationConfig struct {
	DatabasePath      string                      `json:"database_path"`
	Chat              bool                        `json:"chat"`
	BrowserBridge     bool                        `json:"browser_bridge"`
	SessionKeyEnv     string                      `json:"session_key_env"`
	SessionTTLSeconds int                         `json:"session_ttl_seconds,omitempty"`
	AllowedOrigins    []string                    `json:"allowed_origins,omitempty"`
	Integrations      map[string]WebProfileConfig `json:"integrations"`
}

// WebProfileConfig binds an integration alias to a pack and optional frontend profile.
type WebProfileConfig struct {
	PackID              string `json:"pack_id,omitempty"`
	FrontendProfilePath string `json:"frontend_profile_path,omitempty"`
}

// FrontendAction declares a host browser handler's contract and execution properties.
type FrontendAction struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	InputSchema      JSON   `json:"input_schema"`
	OutputSchema     JSON   `json:"output_schema"`
	Effect           string `json:"effect"`
	ApprovalRequired bool   `json:"approval_required,omitempty"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty"`
}

// FrontendProfile pins browser actions, context schema and a handler version.
type FrontendProfile struct {
	Schema         string           `json:"schema"`
	Version        string           `json:"version"`
	HandlerVersion string           `json:"handler_version"`
	ContextSchema  JSON             `json:"context_schema"`
	Actions        []FrontendAction `json:"actions"`
}

// WebHash returns the canonical JSON digest used for request and integration identities, or an empty string for invalid values.
func WebHash(v any) string {
	b, err := CanonicalJSON(v)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// BrowserSession is the persisted owner-bound browser generation and confirmed page snapshot.
type BrowserSession struct {
	ID             string   `json:"id"`
	IntegrationID  string   `json:"integration_id"`
	ProfileDigest  string   `json:"profile_digest"`
	Generation     int      `json:"generation"`
	HandlerVersion string   `json:"handler_version"`
	Handlers       []string `json:"handlers"`
	// Context is page observation data only. Keep its persisted wire name so
	// existing browser sessions and pinned frontend profiles remain readable.
	Context         JSON    `json:"context"`
	ContextRevision int     `json:"context_revision"`
	LastSeen        float64 `json:"last_seen"`
	KeyHash         string  `json:"key_hash,omitempty"`
	ResumeRequestID string  `json:"resume_request_id,omitempty"`
	Closed          bool    `json:"closed"`
}

// BrowserCommand retains an action's exact invocation and client-reported execution receipt.
type BrowserCommand struct {
	ID              string  `json:"id"`
	RunID           string  `json:"run_id"`
	SessionID       string  `json:"session_id"`
	Generation      int     `json:"generation"`
	ProfileDigest   string  `json:"profile_digest"`
	Action          string  `json:"action"`
	Arguments       JSON    `json:"arguments"`
	ArgumentsSHA256 string  `json:"arguments_sha256"`
	ContextRevision int     `json:"context_revision"`
	Status          string  `json:"status"`
	ExpiresAt       float64 `json:"expires_at"`
	Result          JSON    `json:"result,omitempty"`
	ErrorCode       string  `json:"error_code,omitempty"`
}

// WebRunBinding links an authorized run to its integration and browser session.
type WebRunBinding struct {
	RunID            string `json:"run_id"`
	IntegrationID    string `json:"integration_id"`
	SessionID        string `json:"session_id"`
	Generation       int    `json:"generation"`
	ProfileDigest    string `json:"profile_digest"`
	RequestID        string `json:"request_id"`
	ObservedRevision int    `json:"observed_revision"`
}

// ChatConversation retains an owner-scoped integration and active message identity.
type ChatConversation struct {
	ID            string  `json:"id"`
	IntegrationID string  `json:"integration_id"`
	CreatedAt     float64 `json:"created_at"`
}

// ChatInput is an accepted response to a task's request for missing information.
// Its prompt and text are projected from the framework-owned run checkpoint.
type ChatInput struct {
	Field  string `json:"field,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Text   string `json:"text"`
}

// ChatMessage retains queued user text and the framework's persisted result projection.
type ChatMessage struct {
	ContextSelection *ConversationContextSelection `json:"context_selection,omitempty"`
	Sources          []RunSource                   `json:"sources,omitempty"`
	ID               string                        `json:"id"`
	ClientID         string                        `json:"client_id"`
	Text             string                        `json:"text"`
	ConversationID   string                        `json:"conversation_id"`
	SessionID        string                        `json:"session_id,omitempty"`
	RunID            string                        `json:"run_id"`
	Status           string                        `json:"status"`
	Instruction      string                        `json:"instruction,omitempty"`
	ErrorCode        string                        `json:"error_code,omitempty"`
	AnswerMarkdown   string                        `json:"answer_markdown,omitempty"`
	ResultRefs       []ResultObjectRef             `json:"result_refs,omitempty"`
	InputHistory     []ChatInput                   `json:"input_history,omitempty"`
	CreatedAt        float64                       `json:"created_at"`
	Run              map[string]any                `json:"run,omitempty"`
}
