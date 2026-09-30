package agenstra

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrStoreConflict = errors.New("store conflict")
	ErrRunNotFound   = errors.New("run not found")
	ErrLeaseLost     = errors.New("lease lost")
)

type StoredRun struct {
	RunID           string         `json:"run_id"`
	OwnerID         string         `json:"owner_id"`
	PackID          string         `json:"pack_id"`
	Status          string         `json:"status"`
	State           map[string]any `json:"state"`
	Revision        int            `json:"revision"`
	NextWakeAt      *float64       `json:"next_wake_at"`
	LeaseToken      string         `json:"lease_token"`
	LeaseUntil      *float64       `json:"lease_until"`
	CreatedAt       float64        `json:"created_at"`
	UpdatedAt       float64        `json:"updated_at"`
	CancelRequested bool           `json:"cancel_requested"`
}

// SQLiteStore retains the v1 database schema used by the original host.
// IMMEDIATE transactions serialize claims and checkpoint writes; fencing tokens
// prevent an expired worker from committing state.
type SQLiteStore struct {
	Path  string
	DB    *sql.DB
	Clock func() float64
}

func unixNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("SQLiteStore requires a disk path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: abs}).String() + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(30000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &SQLiteStore{Path: path, DB: db, Clock: unixNow}, nil
}

func (s *SQLiteStore) now() float64 {
	if s.Clock != nil {
		return s.Clock()
	}
	return unixNow()
}
func (s *SQLiteStore) Close() error { return s.DB.Close() }
func (s *SQLiteStore) Initialize() error {
	var mode string
	if err := s.DB.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return errors.New("SQLite store requires WAL journal mode")
	}
	return s.write(func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if version > 1 {
			return fmt.Errorf("unsupported SQLite store schema version %d", version)
		}
		if version == 1 {
			return nil
		}
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS runs (
		 run_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, pack_id TEXT NOT NULL,
		 status TEXT NOT NULL, state_json TEXT NOT NULL, revision INTEGER NOT NULL,
		 next_wake_at REAL, lease_token TEXT, lease_until REAL, created_at REAL NOT NULL,
		 updated_at REAL NOT NULL, cancel_requested INTEGER NOT NULL DEFAULT 0);
		 CREATE INDEX IF NOT EXISTS runs_due ON runs(status,next_wake_at,lease_until);
		 CREATE INDEX IF NOT EXISTS runs_owner ON runs(owner_id,created_at);
		 CREATE TABLE IF NOT EXISTS invocations (run_id TEXT NOT NULL REFERENCES runs(run_id),
		 invocation_id TEXT NOT NULL,payload_json TEXT NOT NULL,PRIMARY KEY(run_id,invocation_id));
		 CREATE TABLE IF NOT EXISTS artifacts (run_id TEXT NOT NULL REFERENCES runs(run_id),
		 artifact_id TEXT NOT NULL,payload_json TEXT NOT NULL,sha256 TEXT NOT NULL,PRIMARY KEY(run_id,artifact_id));
		 CREATE TABLE IF NOT EXISTS events (sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		 run_id TEXT NOT NULL REFERENCES runs(run_id),created_at REAL NOT NULL,event_json TEXT NOT NULL);
		 CREATE INDEX IF NOT EXISTS events_run ON events(run_id,sequence);
		 PRAGMA user_version=1;`)
		return err
	})
}

func (s *SQLiteStore) write(action func(*sql.Tx) error) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = action(tx); err != nil {
		return err
	}
	return tx.Commit()
}

type sqlQueryer interface{ QueryRow(string, ...any) *sql.Row }

const runColumns = "run_id,owner_id,pack_id,status,state_json,revision,next_wake_at,lease_token,lease_until,created_at,updated_at,cancel_requested"

type scanner interface{ Scan(...any) error }

func decodeObject(data string) (map[string]any, error) {
	var result map[string]any
	d := json.NewDecoder(bytes.NewBufferString(data))
	d.UseNumber()
	if err := d.Decode(&result); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("stored JSON must be an object")
	}
	return result, nil
}
func scanRun(row scanner) (StoredRun, error) {
	var r StoredRun
	var state string
	var wake, lease sql.NullFloat64
	var token sql.NullString
	err := row.Scan(&r.RunID, &r.OwnerID, &r.PackID, &r.Status, &state, &r.Revision, &wake, &token, &lease, &r.CreatedAt, &r.UpdatedAt, &r.CancelRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrRunNotFound
	}
	if err != nil {
		return r, err
	}
	if wake.Valid {
		r.NextWakeAt = &wake.Float64
	}
	if lease.Valid {
		r.LeaseUntil = &lease.Float64
	}
	r.LeaseToken = token.String
	r.State, err = decodeObject(state)
	return r, err
}
func owned(q sqlQueryer, runID, ownerID string) (StoredRun, error) {
	return scanRun(q.QueryRow("SELECT "+runColumns+" FROM runs WHERE run_id=? AND owner_id=?", runID, ownerID))
}
func (s *SQLiteStore) leased(tx *sql.Tx, runID, ownerID, token string) (StoredRun, error) {
	r, err := owned(tx, runID, ownerID)
	if err != nil {
		return r, err
	}
	if token == "" || r.LeaseToken != token || r.LeaseUntil == nil || *r.LeaseUntil <= s.now() {
		return r, ErrLeaseLost
	}
	return r, nil
}
func (s *SQLiteStore) CreateRun(ownerID, packID string, state map[string]any, runID string) (r StoredRun, err error) {
	if runID == "" {
		runID = NewID()
	}
	payload, err := CanonicalJSON(state)
	if err != nil {
		return r, err
	}
	err = s.write(func(tx *sql.Tx) error {
		var count int
		if e := tx.QueryRow("SELECT count(*) FROM runs WHERE run_id=?", runID).Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return ErrStoreConflict
		}
		now := s.now()
		if _, e := tx.Exec("INSERT INTO runs(run_id,owner_id,pack_id,status,state_json,revision,created_at,updated_at) VALUES(?,?,?,'queued',?,0,?,?)", runID, ownerID, packID, string(payload), now, now); e != nil {
			return e
		}
		var e error
		r, e = owned(tx, runID, ownerID)
		return e
	})
	return
}
func (s *SQLiteStore) GetRun(runID, ownerID string) (StoredRun, error) {
	return owned(s.DB, runID, ownerID)
}
func (s *SQLiteStore) Claim(runID, ownerID string, seconds float64) (r StoredRun, err error) {
	if seconds <= 0 {
		return r, errors.New("lease_seconds must be positive")
	}
	err = s.write(func(tx *sql.Tx) error {
		var e error
		r, e = owned(tx, runID, ownerID)
		if e != nil {
			return e
		}
		now := s.now()
		if r.LeaseUntil != nil && *r.LeaseUntil > now {
			return ErrStoreConflict
		}
		if _, e = tx.Exec("UPDATE runs SET lease_token=?,lease_until=?,updated_at=? WHERE run_id=?", NewID(), now+seconds, now, runID); e != nil {
			return e
		}
		r, e = owned(tx, runID, ownerID)
		return e
	})
	return
}
func (s *SQLiteStore) Renew(runID, ownerID, token string, seconds float64) error {
	if seconds <= 0 {
		return errors.New("lease_seconds must be positive")
	}
	return s.write(func(tx *sql.Tx) error {
		if _, e := s.leased(tx, runID, ownerID, token); e != nil {
			return e
		}
		now := s.now()
		_, e := tx.Exec("UPDATE runs SET lease_until=?,updated_at=? WHERE run_id=?", now+seconds, now, runID)
		return e
	})
}
func (s *SQLiteStore) Release(runID, ownerID, token string) error {
	return s.write(func(tx *sql.Tx) error {
		if _, e := s.leased(tx, runID, ownerID, token); e != nil {
			return e
		}
		_, e := tx.Exec("UPDATE runs SET lease_token=NULL,lease_until=NULL,updated_at=? WHERE run_id=?", s.now(), runID)
		return e
	})
}
func (s *SQLiteStore) RequestCancel(runID, ownerID string) (r StoredRun, err error) {
	err = s.write(func(tx *sql.Tx) error {
		var e error
		r, e = owned(tx, runID, ownerID)
		if e != nil {
			return e
		}
		if !r.CancelRequested {
			now := s.now()
			if _, e = tx.Exec("UPDATE runs SET cancel_requested=1,updated_at=? WHERE run_id=?", now, runID); e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT INTO events(run_id,created_at,event_json) VALUES(?,?,?)", runID, now, `{"kind":"cancel_requested"}`); e != nil {
				return e
			}
		}
		r, e = owned(tx, runID, ownerID)
		return e
	})
	return
}
func (s *SQLiteStore) Checkpoint(runID, ownerID, token string, state map[string]any, status string, wake *float64, invocations []map[string]any, artifacts map[string]map[string]any, events []map[string]any) (r StoredRun, err error) {
	payload, err := CanonicalJSON(state)
	if err != nil {
		return r, err
	}
	err = s.write(func(tx *sql.Tx) error {
		if _, e := s.leased(tx, runID, ownerID, token); e != nil {
			return e
		}
		now := s.now()
		if _, e := tx.Exec("UPDATE runs SET state_json=?,status=?,next_wake_at=?,revision=revision+1,updated_at=? WHERE run_id=?", string(payload), status, wake, now, runID); e != nil {
			return e
		}
		for _, i := range invocations {
			id, ok := i["invocation_id"].(string)
			if !ok || id == "" {
				return errors.New("invocation_id must be a nonempty string")
			}
			b, e := CanonicalJSON(i)
			if e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT INTO invocations VALUES(?,?,?) ON CONFLICT(run_id,invocation_id) DO UPDATE SET payload_json=excluded.payload_json", runID, id, string(b)); e != nil {
				return e
			}
		}
		for id, a := range artifacts {
			b, e := CanonicalJSON(a)
			if e != nil {
				return e
			}
			sum := sha256.Sum256(b)
			digest := hex.EncodeToString(sum[:])
			var prior string
			e = tx.QueryRow("SELECT sha256 FROM artifacts WHERE run_id=? AND artifact_id=?", runID, id).Scan(&prior)
			if e == nil {
				if prior != digest {
					return ErrStoreConflict
				}
				continue
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if _, e = tx.Exec("INSERT INTO artifacts VALUES(?,?,?,?)", runID, id, string(b), digest); e != nil {
				return e
			}
		}
		for _, event := range events {
			b, e := CanonicalJSON(event)
			if e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT INTO events(run_id,created_at,event_json) VALUES(?,?,?)", runID, now, string(b)); e != nil {
				return e
			}
		}
		var e error
		r, e = owned(tx, runID, ownerID)
		return e
	})
	return
}
func (s *SQLiteStore) GetInvocation(runID, invocationID, ownerID string) (map[string]any, error) {
	if _, e := s.GetRun(runID, ownerID); e != nil {
		return nil, e
	}
	var payload string
	e := s.DB.QueryRow("SELECT payload_json FROM invocations WHERE run_id=? AND invocation_id=?", runID, invocationID).Scan(&payload)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return decodeObject(payload)
}
func (s *SQLiteStore) ListInvocations(runID, ownerID string) ([]map[string]any, error) {
	if _, e := s.GetRun(runID, ownerID); e != nil {
		return nil, e
	}
	rows, e := s.DB.Query("SELECT payload_json FROM invocations WHERE run_id=? ORDER BY rowid", runID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var payload string
		if e = rows.Scan(&payload); e != nil {
			return nil, e
		}
		v, e := decodeObject(payload)
		if e != nil {
			return nil, e
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *SQLiteStore) GetArtifact(runID, artifactID, ownerID string) (map[string]any, error) {
	if _, e := s.GetRun(runID, ownerID); e != nil {
		return nil, e
	}
	var payload string
	e := s.DB.QueryRow("SELECT payload_json FROM artifacts WHERE run_id=? AND artifact_id=?", runID, artifactID).Scan(&payload)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if e != nil {
		return nil, e
	}
	return decodeObject(payload)
}
func (s *SQLiteStore) ListEvents(runID, ownerID string, after, limit int) ([]map[string]any, error) {
	if _, e := s.GetRun(runID, ownerID); e != nil {
		return nil, e
	}
	rows, e := s.DB.Query("SELECT sequence,created_at,event_json FROM events WHERE run_id=? AND sequence>? ORDER BY sequence LIMIT ?", runID, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var sequence int
		var created float64
		var payload string
		if e = rows.Scan(&sequence, &created, &payload); e != nil {
			return nil, e
		}
		v, e := decodeObject(payload)
		if e != nil {
			return nil, e
		}
		result = append(result, map[string]any{"sequence": sequence, "created_at": created, "event": v})
	}
	return result, rows.Err()
}
func (s *SQLiteStore) queryRuns(query string, args ...any) ([]StoredRun, error) {
	rows, e := s.DB.Query(query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []StoredRun{}
	for rows.Next() {
		r, e := scanRun(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func (s *SQLiteStore) DueRuns(limit int) ([]StoredRun, error) {
	now := s.now()
	return s.queryRuns("SELECT "+runColumns+" FROM runs WHERE (lease_until IS NULL OR lease_until<=?) AND (status IN ('queued','running') OR (cancel_requested=1 AND status NOT IN ('completed','failed','cancelled')) OR (status='waiting' AND next_wake_at IS NOT NULL AND next_wake_at<=?)) ORDER BY created_at,run_id LIMIT ?", now, now, limit)
}
func (s *SQLiteStore) ListRuns(ownerID string, limit int) ([]StoredRun, error) {
	return s.queryRuns("SELECT "+runColumns+" FROM runs WHERE owner_id=? ORDER BY created_at DESC,run_id LIMIT ?", ownerID, limit)
}
