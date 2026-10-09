package runstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
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

func scanMemory(row scanner) (agentcontract.Memory, error) {
	var raw string
	var m agentcontract.Memory
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return m, agentcontract.NewHostError("not_found")
		}
		return m, err
	}
	err := agentcontract.DecodeDocument(raw, &m)
	return m, err
}

// GetMemory reads a memory under its owner and user-or-pack scope. Host policy checks precede storage access.
func (s *SQLiteStore) GetMemory(owner, pack, id string) (agentcontract.Memory, error) {
	return scanMemory(s.DB.QueryRow("SELECT payload FROM memories WHERE id=? AND owner=? AND (scope='' OR scope=?)", id, owner, pack))
}

// ListMemories lists owner-scoped memories with the requested pagination.
func (s *SQLiteStore) ListMemories(owner, pack string, limit, offset int) (result []agentcontract.Memory, resultErr error) {
	rows, err := s.DB.Query("SELECT payload FROM memories WHERE owner=? AND (scope='' OR scope=?) ORDER BY scope,topic LIMIT ? OFFSET ?", owner, pack, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	items := []agentcontract.Memory{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}

func writeMemory(tx *sql.Tx, owner string, m agentcontract.Memory) error {
	raw, err := agentcontract.EncodeDocument(m)
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

func invalidateMemory(tx *sql.Tx, m agentcontract.Memory) error {
	floor, err := evidenceFloor(tx, m.ID)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE memories SET evidence_floor=?,invalidated_revision=? WHERE id=?", floor, m.Revision, m.ID)
	return err
}

func forgetStoredMemory(tx *sql.Tx, owner string, m agentcontract.Memory, now float64, origin string) (agentcontract.Memory, error) {
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

// SetMemory saves an owner-scoped manual memory with revision checking and retained history.
func (s *SQLiteStore) SetMemory(owner, pack string, u agentcontract.MemoryUpdate) (m agentcontract.Memory, err error) {
	err = s.Transaction(func(tx *sql.Tx) error {
		current, e := scanMemory(tx.QueryRow("SELECT payload FROM memories WHERE owner=? AND scope=? AND topic=?", owner, memoryScope(u.Scope, pack), u.Key))
		if e != nil && agentcontract.ErrorCode(e) != "not_found" {
			return e
		}
		if e == nil && current.Revision != u.Revision || e != nil && u.Revision != 0 {
			return agentcontract.NewHostError("revision_conflict")
		}
		now := s.Now()
		m = current
		if m.ID == "" {
			m = agentcontract.Memory{ID: agentcontract.NewID(), Scope: u.Scope, Key: u.Key, CreatedAt: now}
		}
		m.PackID = memoryScope(u.Scope, pack)
		m.Value = u.Value
		m.Kind = u.Kind
		m.Status = "active"
		m.Origin = "manual"
		m.Revision++
		m.EvidenceCount = 0
		m.SourceID = "host:" + agentcontract.NewID()
		m.Quote = u.Value
		m.UpdatedAt = now
		if e = writeMemory(tx, owner, m); e != nil {
			return e
		}
		return invalidateMemory(tx, m)
	})
	return
}

// ForgetMemory marks the selected owner-scoped memory forgotten under its expected revision.
func (s *SQLiteStore) ForgetMemory(owner, pack, id string, revision int) (m agentcontract.Memory, err error) {
	err = s.Transaction(func(tx *sql.Tx) error {
		var e error
		m, e = scanMemory(tx.QueryRow("SELECT payload FROM memories WHERE owner=? AND id=? AND (scope='' OR scope=?)", owner, id, pack))
		if e != nil {
			return e
		}
		if m.Revision != revision {
			return agentcontract.NewHostError("revision_conflict")
		}
		m, e = forgetStoredMemory(tx, owner, m, s.Now(), "manual")
		return e
	})
	return
}

// MemoryHistory reads the retained owner-scoped revisions and supporting evidence.
func (s *SQLiteStore) MemoryHistory(owner, pack, id string) (result agentcontract.MemoryHistory, resultErr error) {
	out := agentcontract.MemoryHistory{Revisions: []agentcontract.Memory{}, Evidence: []agentcontract.MemoryEvidence{}}

	// Ownership is part of each query, including concurrent delete/update races.
	rows, err := s.DB.Query("SELECT r.payload FROM memory_revisions r JOIN memories m ON m.id=r.memory WHERE m.id=? AND m.owner=? AND (m.scope='' OR m.scope=?) ORDER BY r.revision DESC LIMIT 100", id, owner, pack)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		m, e := scanMemory(rows)
		if e != nil {
			return out, errors.Join(e, rows.Close())
		}
		out.Revisions = append(out.Revisions, m)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	rows, err = s.DB.Query("SELECT e.payload FROM memory_evidence e JOIN memories m ON m.id=e.memory WHERE m.id=? AND m.owner=? AND (m.scope='' OR m.scope=?) ORDER BY e.sequence DESC LIMIT 100", id, owner, pack)
	if err != nil {
		return out, err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	for rows.Next() {
		var raw string
		var evidence agentcontract.MemoryEvidence
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if err = agentcontract.DecodeDocument(raw, &evidence); err != nil {
			return out, err
		}
		out.Evidence = append(out.Evidence, evidence)
	}
	return out, rows.Err()
}

// MemoryInputDone reports whether a learning input was already consumed by this run.
func (s *SQLiteStore) MemoryInputDone(owner string, input MemoryInput) (bool, error) {
	var hash string
	err := s.DB.QueryRow("SELECT hash FROM memory_inputs WHERE owner=? AND source=?", owner, input.ID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != agentcontract.WebHash(input.Text) {
		return false, agentcontract.NewHostError("memory_source_conflict")
	}
	return true, nil
}

// ApplyMemoryInput atomically validates the lease, applies learned preferences and records input consumption.
func (s *SQLiteStore) ApplyMemoryInput(run agentcontract.StoredRun, input MemoryInput, proposals []agentcontract.MemoryProposal, code string) error {
	return s.Transaction(func(tx *sql.Tx) error {
		if _, err := s.leased(tx, run.RunID, run.OwnerID, run.LeaseToken); err != nil {
			return err
		}
		result, err := tx.Exec("INSERT INTO memory_inputs(owner,source,hash,error_code) VALUES(?,?,?,?) ON CONFLICT(owner,source) DO NOTHING", run.OwnerID, input.ID, agentcontract.WebHash(input.Text), code)
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
			if e != nil && agentcontract.ErrorCode(e) != "not_found" {
				return e
			}
			now := s.Now()
			if p.Mode == "forget" {
				if e == nil {
					if _, e = forgetStoredMemory(tx, run.OwnerID, m, now, "explicit"); e != nil {
						return e
					}
				}
				continue
			}
			if m.ID == "" {
				m = agentcontract.Memory{ID: agentcontract.NewID(), Scope: p.Scope, PackID: memoryScope(p.Scope, run.PackID), Key: p.Key, Value: p.Value, Kind: p.Kind, Status: "candidate", Origin: "inferred", CreatedAt: now}
			}

			// Create the identity before its FK-bound evidence. The transaction is atomic.
			if m.Revision == 0 {
				m.Revision = 1
				if e = writeMemory(tx, run.OwnerID, m); e != nil {
					return e
				}
			}
			evidence := agentcontract.MemoryEvidence{SourceID: input.ID, Quote: p.Quote, Value: p.Value, Mode: p.Mode, CreatedAt: now}
			raw, e := agentcontract.EncodeDocument(evidence)
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

// VisibleMemorySnapshot filters a frozen preference snapshot against currently active revisions.
func (s *SQLiteStore) VisibleMemorySnapshot(owner, pack string, views []agentcontract.MemoryView) ([]agentcontract.MemoryView, error) {
	out := []agentcontract.MemoryView{}
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
		var current agentcontract.Memory
		if err = agentcontract.DecodeDocument(raw, &current); err != nil {
			return nil, err
		}
		if current.Status == "active" && v.Revision >= invalidated && current.Value == v.Value {
			out = append(out, v)
		}
	}
	return out, nil
}

// MemoryInputError records a bounded learning error and input consumption under the live lease.
func (s *SQLiteStore) MemoryInputError(owner, source string) (string, error) {
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

// MemoryInput identifies a saved learning input so retries do not count it as independent evidence.
type MemoryInput struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

const memoryHabitEvidence = 3
