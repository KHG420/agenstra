package runstore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func testStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, e := NewSQLiteStore(filepath.Join(t.TempDir(), "runs.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Initialize(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func testRun(t *testing.T, s *SQLiteStore, id string) agentcontract.StoredRun {
	t.Helper()
	r, e := s.CreateRun("alice", "records", map[string]any{"value": "original"}, id)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestStoreReopenOwnerScope(t *testing.T) {
	s := testStore(t)
	r := testRun(t, s, agentcontract.NewID())
	if _, e := s.GetRun(r.RunID, "bob"); !errors.Is(e, ErrRunNotFound) {
		t.Fatalf("owner scope: %v", e)
	}
	second, e := NewSQLiteStore(s.Path)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(second.Close)
	if e = second.Initialize(); e != nil {
		t.Fatal(e)
	}
	got, e := second.GetRun(r.RunID, "alice")
	if e != nil || got.State["value"] != "original" {
		t.Fatalf("reopen: %+v %v", got, e)
	}
}

func TestStoreCompetingClaimsAndFencing(t *testing.T) {
	s := testStore(t)
	r := testRun(t, s, agentcontract.NewID())
	now := 1000.
	s.Clock = func() float64 { return now }
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := []agentcontract.StoredRun{}
	for range 8 {
		wg.Go(func() {
			got, e := s.Claim(r.RunID, "alice", 60)
			if e == nil {
				mu.Lock()
				wins = append(wins, got)
				mu.Unlock()
			} else if !errors.Is(e, ErrStoreConflict) {
				t.Errorf("claim: %v", e)
			}
		})
	}
	wg.Wait()
	if len(wins) != 1 {
		t.Fatalf("claims=%d", len(wins))
	}
	old := wins[0]
	now = 1061.
	latest, e := s.Claim(r.RunID, "alice", 60)
	if e != nil {
		t.Fatal(e)
	}
	if latest.LeaseToken == old.LeaseToken {
		t.Fatal("token reused")
	}
	if e = s.Renew(r.RunID, "alice", old.LeaseToken, 60); !errors.Is(e, ErrLeaseLost) {
		t.Fatalf("renew old: %v", e)
	}
	if _, e = s.Checkpoint(r.RunID, "alice", old.LeaseToken, map[string]any{}, "running", nil, nil, nil, nil); !errors.Is(e, ErrLeaseLost) {
		t.Fatalf("old write: %v", e)
	}
}

func TestStoreAtomicArtifacts(t *testing.T) {
	s := testStore(t)
	r := testRun(t, s, agentcontract.NewID())
	r, e := s.Claim(r.RunID, "alice", 60)
	if e != nil {
		t.Fatal(e)
	}
	artifacts := map[string]map[string]any{"fact-1": {"data": "original"}}
	r, e = s.Checkpoint(r.RunID, "alice", r.LeaseToken, map[string]any{"value": "saved"}, "running", nil, []map[string]any{{"invocation_id": "call-1"}}, artifacts, []map[string]any{{"kind": "saved"}})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Checkpoint(r.RunID, "alice", r.LeaseToken, map[string]any{"value": "bad"}, "failed", nil, nil, map[string]map[string]any{"fact-1": {"data": "modified"}}, []map[string]any{{"kind": "bad"}})
	if !errors.Is(e, ErrStoreConflict) {
		t.Fatalf("immutable: %v", e)
	}
	got, e := s.GetRun(r.RunID, "alice")
	if e != nil || got.Revision != r.Revision || got.State["value"] != "saved" {
		t.Fatalf("atomicity: %+v %v", got, e)
	}
	events, e := s.ListEvents(r.RunID, "alice", 0, 10)
	if e != nil || len(events) != 1 {
		t.Fatalf("events: %v %v", events, e)
	}
	if _, e = s.GetArtifact(r.RunID, "fact-1", "bob"); !errors.Is(e, ErrRunNotFound) {
		t.Fatalf("artifact scope: %v", e)
	}
}

func TestCancelSurvivesCheckpointAndWakesPauses(t *testing.T) {
	for _, status := range []string{"needs_input", "needs_approval", "needs_authorization", "needs_reconciliation", "waiting"} {
		t.Run(status, func(t *testing.T) {
			s := testStore(t)
			r := testRun(t, s, agentcontract.NewID())
			now := 1000.
			s.Clock = func() float64 { return now }
			r, e := s.Claim(r.RunID, "alice", 60)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.RequestCancel(r.RunID, "alice"); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Checkpoint(r.RunID, "alice", r.LeaseToken, map[string]any{}, status, nil, nil, nil, nil); e != nil {
				t.Fatal(e)
			}
			due, e := s.DueRuns(10)
			if e != nil || len(due) != 0 {
				t.Fatalf("live lease: %v %v", due, e)
			}
			now = 1061.
			due, e = s.DueRuns(10)
			if e != nil || len(due) != 1 || !due[0].CancelRequested {
				t.Fatalf("pause wake: %v %v", due, e)
			}
		})
	}
}
