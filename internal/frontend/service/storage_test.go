package service

import (
	"path/filepath"
	"testing"

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
