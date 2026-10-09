package service

import (
	"database/sql"
	"errors"
	"fmt"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

// WebStore owns only optional integration data, never the v1 run schema.
type WebStore struct{ store *runstore.SQLiteStore }

// NewWebStore opens and initializes the optional integration database.
// The caller must close it after all integration requests have stopped.
func NewWebStore(path string) (*WebStore, error) {
	s, e := runstore.NewSQLiteStore(path)
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
		return nil, errors.Join(e, s.Close())
	}
	return w, nil
}

// Close releases the integration database connection.
func (w *WebStore) Close() error { return w.store.Close() }

func webLoad(q sqlQueryer, table, id, owner string, v any) error {
	// table is always an internal constant, never supplied by a request.
	var raw string
	e := q.QueryRow("SELECT payload FROM "+table+" WHERE id=? AND owner=?", id, owner).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return agentcontract.NewHostError("not_found")
	}
	if e != nil {
		return e
	}
	return agentcontract.DecodeDocument(raw, v)
}

func webSave(tx *sql.Tx, table, id string, v any) error {
	raw, e := agentcontract.EncodeDocument(v)
	if e != nil {
		return e
	}
	_, e = tx.Exec("UPDATE "+table+" SET payload=? WHERE id=?", raw, id)
	return e
}

func webInsert(tx *sql.Tx, table, id, owner string, v any) error {
	raw, e := agentcontract.EncodeDocument(v)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO "+table+"(id,owner,payload) VALUES(?,?,?)", id, owner, raw)
	return e
}

func (w *WebStore) binding(owner, run string) (agentcontract.WebRunBinding, error) {
	var b agentcontract.WebRunBinding
	var raw string
	e := w.store.DB.QueryRow("SELECT payload FROM web_bindings WHERE run=? AND owner=?", run, owner).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return b, agentcontract.NewHostError("browser_binding_missing")
	}
	if e == nil {
		e = agentcontract.DecodeDocument(raw, &b)
	}
	return b, e
}

func (w *WebStore) commands(tx *sql.Tx, owner, session string) (output []agentcontract.BrowserCommand, resultErr error) {
	rows, e := tx.Query("SELECT payload FROM web_commands WHERE owner=? AND session=? ORDER BY sequence", owner, session)
	if e != nil {
		return nil, e
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	result := []agentcontract.BrowserCommand{}
	for rows.Next() {
		var raw string
		var c agentcontract.BrowserCommand
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = agentcontract.DecodeDocument(raw, &c); e != nil {
			return nil, e
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (w *WebStore) messages(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, owner, conversation string) (output []agentcontract.ChatMessage, resultErr error) {
	rows, e := q.Query("SELECT payload FROM web_messages WHERE owner=? AND conversation=? ORDER BY sequence", owner, conversation)
	if e != nil {
		return nil, e
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	result := []agentcontract.ChatMessage{}
	for rows.Next() {
		var raw string
		var m agentcontract.ChatMessage
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = agentcontract.DecodeDocument(raw, &m); e != nil {
			return nil, e
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

type sqlQueryer interface{ QueryRow(string, ...any) *sql.Row }
