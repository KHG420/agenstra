package agenstra

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func initializeMemoryTables(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS memories (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, scope TEXT NOT NULL, topic TEXT NOT NULL,
 payload TEXT NOT NULL, evidence_floor INTEGER NOT NULL DEFAULT 0,
 invalidated_revision INTEGER NOT NULL DEFAULT 0, UNIQUE(owner,scope,topic));
 CREATE INDEX IF NOT EXISTS memories_owner_scope ON memories(owner,scope,topic);
 CREATE TABLE IF NOT EXISTS memory_evidence (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, memory TEXT NOT NULL REFERENCES memories(id),
 source TEXT NOT NULL, value TEXT NOT NULL, payload TEXT NOT NULL, UNIQUE(memory,source));
 CREATE TABLE IF NOT EXISTS memory_revisions (
 memory TEXT NOT NULL REFERENCES memories(id), revision INTEGER NOT NULL, payload TEXT NOT NULL,
 PRIMARY KEY(memory,revision));
 CREATE TABLE IF NOT EXISTS memory_inputs (
 owner TEXT NOT NULL, source TEXT NOT NULL, hash TEXT NOT NULL, error_code TEXT NOT NULL,
 PRIMARY KEY(owner,source));`)
	return err
}
func memoryScope(scope, pack string) string {
	if scope == "pack" {
		return pack
	}
	return ""
}
func scanMemory(row scanner) (Memory, error) {
	var raw string
	var m Memory
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return m, hostError("not_found")
		}
		return m, err
	}
	err := webDecode(raw, &m)
	return m, err
}
func (s *SQLiteStore) getMemory(owner, pack, id string) (Memory, error) {
	return scanMemory(s.DB.QueryRow("SELECT payload FROM memories WHERE id=? AND owner=? AND (scope='' OR scope=?)", id, owner, pack))
}
func (s *SQLiteStore) listMemories(owner, pack string, limit, offset int) ([]Memory, error) {
	rows, err := s.DB.Query("SELECT payload FROM memories WHERE owner=? AND (scope='' OR scope=?) ORDER BY scope,topic LIMIT ? OFFSET ?", owner, pack, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Memory{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}
func writeMemory(tx *sql.Tx, owner string, m Memory) error {
	raw, err := webJSON(m)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO memories(id,owner,scope,topic,payload) VALUES(?,?,?,?,?) ON CONFLICT(owner,scope,topic) DO UPDATE SET payload=excluded.payload", m.ID, owner, memoryScope(m.Scope, m.PackID), m.Key, raw)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO memory_revisions(memory,revision,payload) VALUES(?,?,?)", m.ID, m.Revision, raw)
	return err
}
func evidenceFloor(tx *sql.Tx, id string) (int64, error) {
	var floor int64
	err := tx.QueryRow("SELECT coalesce(max(sequence),0) FROM memory_evidence WHERE memory=?", id).Scan(&floor)
	return floor, err
}
func invalidateMemory(tx *sql.Tx, m Memory) error {
	floor, err := evidenceFloor(tx, m.ID)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE memories SET evidence_floor=?,invalidated_revision=? WHERE id=?", floor, m.Revision, m.ID)
	return err
}
func forgetStoredMemory(tx *sql.Tx, owner string, m Memory, now float64, origin string) (Memory, error) {
	m.Revision++
	m.Status = "forgotten"
	m.Value = ""
	m.Quote = ""
	m.SourceID = ""
	m.Origin = origin
	m.EvidenceCount = 0
	m.UpdatedAt = now
	if err := invalidateMemory(tx, m); err != nil {
		return m, err
	}
	if _, err := tx.Exec("DELETE FROM memory_evidence WHERE memory=?", m.ID); err != nil {
		return m, err
	}
	if _, err := tx.Exec("DELETE FROM memory_revisions WHERE memory=?", m.ID); err != nil {
		return m, err
	}
	return m, writeMemory(tx, owner, m)
}
func (s *SQLiteStore) setMemory(owner, pack string, u MemoryUpdate) (m Memory, err error) {
	err = s.write(func(tx *sql.Tx) error {
		current, e := scanMemory(tx.QueryRow("SELECT payload FROM memories WHERE owner=? AND scope=? AND topic=?", owner, memoryScope(u.Scope, pack), u.Key))
		if e != nil && ErrorCode(e) != "not_found" {
			return e
		}
		if e == nil && current.Revision != u.Revision || e != nil && u.Revision != 0 {
			return hostError("revision_conflict")
		}
		now := s.now()
		m = current
		if m.ID == "" {
			m = Memory{ID: NewID(), Scope: u.Scope, Key: u.Key, CreatedAt: now}
		}
		m.PackID = memoryScope(u.Scope, pack)
		m.Value = u.Value
		m.Kind = u.Kind
		m.Status = "active"
		m.Origin = "manual"
		m.Revision++
		m.EvidenceCount = 0
		m.SourceID = "host:" + NewID()
		m.Quote = u.Value
		m.UpdatedAt = now
		if e = writeMemory(tx, owner, m); e != nil {
			return e
		}
		return invalidateMemory(tx, m)
	})
	return
}
func (s *SQLiteStore) forgetMemory(owner, pack, id string, revision int) (m Memory, err error) {
	err = s.write(func(tx *sql.Tx) error {
		var e error
		m, e = scanMemory(tx.QueryRow("SELECT payload FROM memories WHERE owner=? AND id=? AND (scope='' OR scope=?)", owner, id, pack))
		if e != nil {
			return e
		}
		if m.Revision != revision {
			return hostError("revision_conflict")
		}
		m, e = forgetStoredMemory(tx, owner, m, s.now(), "manual")
		return e
	})
	return
}
func (s *SQLiteStore) memoryHistory(owner, pack, id string) (MemoryHistory, error) {
	out := MemoryHistory{Revisions: []Memory{}, Evidence: []MemoryEvidence{}}
	// Ownership is part of each query, including concurrent delete/update races.
	rows, err := s.DB.Query("SELECT r.payload FROM memory_revisions r JOIN memories m ON m.id=r.memory WHERE m.id=? AND m.owner=? AND (m.scope='' OR m.scope=?) ORDER BY r.revision DESC LIMIT 100", id, owner, pack)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		m, e := scanMemory(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Revisions = append(out.Revisions, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = s.DB.Query("SELECT e.payload FROM memory_evidence e JOIN memories m ON m.id=e.memory WHERE m.id=? AND m.owner=? AND (m.scope='' OR m.scope=?) ORDER BY e.sequence DESC LIMIT 100", id, owner, pack)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var evidence MemoryEvidence
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if err = webDecode(raw, &evidence); err != nil {
			return out, err
		}
		out.Evidence = append(out.Evidence, evidence)
	}
	return out, rows.Err()
}
func (s *SQLiteStore) memoryInputDone(owner string, input memoryInput) (bool, error) {
	var hash string
	err := s.DB.QueryRow("SELECT hash FROM memory_inputs WHERE owner=? AND source=?", owner, input.ID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != webHash(input.Text) {
		return false, hostError("memory_source_conflict")
	}
	return true, nil
}
func (s *SQLiteStore) applyMemoryInput(run StoredRun, input memoryInput, proposals []MemoryProposal, code string) error {
	return s.write(func(tx *sql.Tx) error {
		if _, err := s.leased(tx, run.RunID, run.OwnerID, run.LeaseToken); err != nil {
			return err
		}
		result, err := tx.Exec("INSERT INTO memory_inputs(owner,source,hash,error_code) VALUES(?,?,?,?) ON CONFLICT(owner,source) DO NOTHING", run.OwnerID, input.ID, webHash(input.Text), code)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		for _, p := range proposals {
			if p.Mode == "temporary" {
				continue
			}
			// Even when a model misses a temporary qualifier, it cannot count this input
			// toward a learned habit. Explicit lasting clauses remain eligible.
			if p.Mode == "habit" && temporaryMemoryInput(input.Text) {
				continue
			}
			m, e := scanMemory(tx.QueryRow("SELECT payload FROM memories WHERE owner=? AND scope=? AND topic=?", run.OwnerID, memoryScope(p.Scope, run.PackID), p.Key))
			if e != nil && ErrorCode(e) != "not_found" {
				return e
			}
			now := s.now()
			if p.Mode == "forget" {
				if e == nil {
					if _, e = forgetStoredMemory(tx, run.OwnerID, m, now, "explicit"); e != nil {
						return e
					}
				}
				continue
			}
			if m.ID == "" {
				m = Memory{ID: NewID(), Scope: p.Scope, PackID: memoryScope(p.Scope, run.PackID), Key: p.Key, Value: p.Value, Kind: p.Kind, Status: "candidate", Origin: "inferred", CreatedAt: now}
			}
			// Create the identity before its FK-bound evidence. The transaction is atomic.
			if m.Revision == 0 {
				m.Revision = 1
				if e = writeMemory(tx, run.OwnerID, m); e != nil {
					return e
				}
			}
			evidence := MemoryEvidence{SourceID: input.ID, Quote: p.Quote, Value: p.Value, Mode: p.Mode, CreatedAt: now}
			raw, e := webJSON(evidence)
			if e != nil {
				return e
			}
			result, e = tx.Exec("INSERT INTO memory_evidence(memory,source,value,payload) VALUES(?,?,?,?) ON CONFLICT(memory,source) DO NOTHING", m.ID, input.ID, p.Value, raw)
			if e != nil {
				return e
			}
			n, e = result.RowsAffected()
			if e != nil {
				return e
			}
			if n == 0 {
				continue
			}
			count := 0
			if e = tx.QueryRow("SELECT count(*) FROM memory_evidence e JOIN memories m ON m.id=e.memory WHERE e.memory=? AND e.value=? AND e.sequence>m.evidence_floor", m.ID, p.Value).Scan(&count); e != nil {
				return e
			}
			activate := p.Mode == "explicit" || count >= memoryHabitEvidence && (m.Status != "active" || m.Origin == "inferred")
			oldValue := m.Value
			if activate {
				m.Value = p.Value
				m.Kind = p.Kind
				m.Status = "active"
				m.Origin = "inferred"
				if p.Mode == "explicit" {
					m.Origin = "explicit"
				}
			}
			// Preserve explicit/manual defaults when a conflicting incidental habit appears.
			if m.Value == p.Value {
				m.SourceID = input.ID
				m.Quote = p.Quote
				m.EvidenceCount = count
			}
			m.Revision++
			m.UpdatedAt = now
			if e = writeMemory(tx, run.OwnerID, m); e != nil {
				return e
			}
			if p.Mode == "explicit" || activate && oldValue != m.Value {
				if e = invalidateMemory(tx, m); e != nil {
					return e
				}
			}
		}
		return nil
	})
}
func temporaryMemoryInput(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"这次", "本次", "临时", "仅此", "this time", "for this task", "just this once", "temporarily"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
func (s *SQLiteStore) visibleMemorySnapshot(owner, pack string, views []MemoryView) ([]MemoryView, error) {
	out := []MemoryView{}
	for _, v := range views {
		var raw string
		var invalidated int
		err := s.DB.QueryRow("SELECT payload,invalidated_revision FROM memories WHERE id=? AND owner=? AND (scope='' OR scope=?)", v.ID, owner, pack).Scan(&raw, &invalidated)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var current Memory
		if err = webDecode(raw, &current); err != nil {
			return nil, err
		}
		if current.Status == "active" && v.Revision >= invalidated && current.Value == v.Value {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *SQLiteStore) memoryInputError(owner, source string) (string, error) {
	var code string
	err := s.DB.QueryRow("SELECT error_code FROM memory_inputs WHERE owner=? AND source=?", owner, source).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("memory input: %w", err)
	}
	return code, nil
}
