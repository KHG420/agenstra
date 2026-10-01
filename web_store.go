package agenstra

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// WebStore owns only optional integration data, never the v1 run schema.
type WebStore struct{ store *SQLiteStore }

func NewWebStore(path string) (*WebStore, error) {
	s, e := NewSQLiteStore(path)
	if e != nil {
		return nil, e
	}
	w := &WebStore{s}
	var mode string
	if e = s.DB.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); e == nil && mode != "wal" {
		e = errors.New("web store requires WAL")
	}
	if e == nil {
		_, e = s.DB.Exec(`CREATE TABLE IF NOT EXISTS web_meta(version INTEGER NOT NULL);
 INSERT INTO web_meta SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM web_meta);
 CREATE TABLE IF NOT EXISTS web_sessions(id TEXT PRIMARY KEY,owner TEXT NOT NULL,payload TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS web_sessions_owner ON web_sessions(owner);
 CREATE TABLE IF NOT EXISTS web_conversations(id TEXT PRIMARY KEY,owner TEXT NOT NULL,payload TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS web_conversations_owner ON web_conversations(owner);
 CREATE TABLE IF NOT EXISTS web_messages(sequence INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT UNIQUE NOT NULL,owner TEXT NOT NULL,conversation TEXT NOT NULL,client_id TEXT NOT NULL,payload TEXT NOT NULL,UNIQUE(conversation,client_id));
 CREATE INDEX IF NOT EXISTS web_messages_conversation ON web_messages(conversation,sequence);
 CREATE TABLE IF NOT EXISTS web_commands(sequence INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT UNIQUE NOT NULL,owner TEXT NOT NULL,session TEXT NOT NULL,run TEXT NOT NULL,payload TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS web_commands_session ON web_commands(session,sequence);
 CREATE INDEX IF NOT EXISTS web_commands_run ON web_commands(run);
 CREATE TABLE IF NOT EXISTS web_bindings(run TEXT PRIMARY KEY,owner TEXT NOT NULL,payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS web_releases(id TEXT PRIMARY KEY,payload TEXT NOT NULL);`)
	}
	var version int
	if e == nil {
		e = s.DB.QueryRow("SELECT version FROM web_meta").Scan(&version)
		if e == nil && version != 1 {
			e = fmt.Errorf("unsupported web schema %d", version)
		}
	}
	if e != nil {
		s.Close()
		return nil, e
	}
	return w, nil
}
func (w *WebStore) Close() error    { return w.store.Close() }
func webJSON(v any) (string, error) { b, e := CanonicalJSON(v); return string(b), e }
func webDecode(raw string, v any) error {
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.UseNumber()
	return d.Decode(v)
}
func webHash(v any) string {
	b, _ := CanonicalJSON(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func webLoad(q sqlQueryer, table, id, owner string, v any) error {
	// table is always an internal constant, never supplied by a request.
	var raw string
	e := q.QueryRow("SELECT payload FROM "+table+" WHERE id=? AND owner=?", id, owner).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return hostError("not_found")
	}
	if e != nil {
		return e
	}
	return webDecode(raw, v)
}
func webSave(tx *sql.Tx, table, id string, v any) error {
	raw, e := webJSON(v)
	if e != nil {
		return e
	}
	_, e = tx.Exec("UPDATE "+table+" SET payload=? WHERE id=?", raw, id)
	return e
}
func webInsert(tx *sql.Tx, table, id, owner string, v any) error {
	raw, e := webJSON(v)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO "+table+"(id,owner,payload) VALUES(?,?,?)", id, owner, raw)
	return e
}

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
	Closed          bool    `json:"closed"`
}
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
type WebRunBinding struct {
	RunID            string `json:"run_id"`
	IntegrationID    string `json:"integration_id"`
	SessionID        string `json:"session_id"`
	Generation       int    `json:"generation"`
	ProfileDigest    string `json:"profile_digest"`
	RequestID        string `json:"request_id"`
	ObservedRevision int    `json:"observed_revision"`
}
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

type ChatMessage struct {
	ID             string         `json:"id"`
	ClientID       string         `json:"client_id"`
	Text           string         `json:"text"`
	ConversationID string         `json:"conversation_id"`
	SessionID      string         `json:"session_id,omitempty"`
	RunID          string         `json:"run_id"`
	Status         string         `json:"status"`
	Instruction    string         `json:"instruction,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	AnswerMarkdown string         `json:"answer_markdown,omitempty"`
	InputHistory   []ChatInput    `json:"input_history,omitempty"`
	CreatedAt      float64        `json:"created_at"`
	Run            map[string]any `json:"run,omitempty"`
}

func (w *WebStore) binding(owner, run string) (WebRunBinding, error) {
	var b WebRunBinding
	var raw string
	e := w.store.DB.QueryRow("SELECT payload FROM web_bindings WHERE run=? AND owner=?", run, owner).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return b, hostError("browser_binding_missing")
	}
	if e == nil {
		e = webDecode(raw, &b)
	}
	return b, e
}
func (w *WebStore) commands(tx *sql.Tx, owner, session string) ([]BrowserCommand, error) {
	rows, e := tx.Query("SELECT payload FROM web_commands WHERE owner=? AND session=? ORDER BY sequence", owner, session)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []BrowserCommand{}
	for rows.Next() {
		var raw string
		var c BrowserCommand
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = webDecode(raw, &c); e != nil {
			return nil, e
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
func (w *WebStore) messages(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, owner, conversation string) ([]ChatMessage, error) {
	rows, e := q.Query("SELECT payload FROM web_messages WHERE owner=? AND conversation=? ORDER BY sequence", owner, conversation)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []ChatMessage{}
	for rows.Next() {
		var raw string
		var m ChatMessage
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = webDecode(raw, &m); e != nil {
			return nil, e
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
