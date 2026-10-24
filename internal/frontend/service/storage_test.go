package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func testStore(t *testing.T) *runstore.SQLiteStore {
	t.Helper()
	var s *runstore.Store
	var e error
	if os.Getenv("AGENSTRA_TEST_STORAGE_BACKEND") == "postgres" {
		s, e = runstore.NewPostgresStore(os.Getenv("AGENSTRA_TEST_POSTGRES_DSN"), "test_runs_"+strings.ReplaceAll(agentcontract.NewID(), "-", ""))
	} else {
		s, e = runstore.NewSQLiteStore(filepath.Join(t.TempDir(), "runs.sqlite3"))
	}
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Initialize(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if s.Postgres {
			if _, err := s.DB.Exec("DROP SCHEMA " + s.Path + " CASCADE"); err != nil {
				t.Error(err)
			}
		}
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
