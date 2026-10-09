package host

import (
	"os"
	"path/filepath"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func testStore(t *testing.T) *runstore.SQLiteStore {
	t.Helper()
	s, e := runstore.NewSQLiteStore(filepath.Join(t.TempDir(), "runs.sqlite3"))
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

func testRun(t *testing.T, s *runstore.SQLiteStore, id string) agentcontract.StoredRun {
	t.Helper()
	r, e := s.CreateRun("alice", "records", map[string]any{"value": "original"}, id)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestPythonV1DatabaseRestoresFactsAndContinues(t *testing.T) {
	s := testStore(t)
	dump, e := os.ReadFile("testdata/python-v1.sql")
	if e != nil {
		t.Fatal(e)
	}

	// Import into a separate empty database because the fixture contains CREATEs.
	if err := s.Close(); err != nil {
		t.Error(err)
	}
	if e = os.Remove(s.Path); e != nil {
		t.Fatal(e)
	}
	s, e = runstore.NewSQLiteStore(s.Path)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(s.Close)
	if _, e = s.DB.Exec("PRAGMA foreign_keys=OFF;\n" + string(dump) + "\nPRAGMA foreign_keys=ON;"); e != nil {
		t.Fatal(e)
	}
	if e = s.Initialize(); e != nil {
		t.Fatal(e)
	}
	h := testHost(t, s, &hostProvider{}, &hostModel{})
	h.Clock = func() float64 { return 1700000001 }
	r, e := h.Get(t.Context(), "cc673326-fcdd-4f40-bec8-e2f388321ef1", "alice")
	if e != nil {
		t.Fatal(e)
	}
	state, e := h.Restore(r)
	if e != nil || len(state.Facts) != 1 {
		t.Fatalf("restore: %+v %v", state, e)
	}
	var digest string
	if e = s.DB.QueryRow("SELECT sha256 FROM artifacts").Scan(&digest); e != nil {
		t.Fatal(e)
	}
	r, e = h.SupplyInput(t.Context(), r.RunID, "alice", "destination", "Shanghai", r.Revision)
	if e != nil || r.Status != "queued" {
		t.Fatalf("input: %+v %v", r, e)
	}
	r, e = h.Drive(t.Context(), r.RunID, "alice")
	if e != nil || r.Status != "completed" {
		t.Fatalf("continue Python run: %s %v", r.Status, e)
	}
	var after string
	if e = s.DB.QueryRow("SELECT sha256 FROM artifacts").Scan(&after); e != nil {
		t.Fatal(e)
	}
	if after != digest {
		t.Fatal("existing artifact modified")
	}
}
