package runstore

import (
	"errors"
	"os"
	"sync"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestPostgresStoreParity(t *testing.T) {
	if os.Getenv("AGENSTRA_TEST_POSTGRES_DSN") == "" {
		t.Skip("set AGENSTRA_TEST_POSTGRES_DSN to a disposable PostgreSQL database")
	}
	t.Setenv("AGENSTRA_TEST_STORAGE_BACKEND", "postgres")
	t.Run("reopen_owner_scope", TestStoreReopenOwnerScope)
	t.Run("claims_and_fencing", TestStoreCompetingClaimsAndFencing)
	t.Run("atomic_artifacts", TestStoreAtomicArtifacts)
	t.Run("durable_cancel", TestCancelSurvivesCheckpointAndWakesPauses)
	t.Run("independent_connections", func(t *testing.T) {
		s := testStore(t)
		other, err := NewPostgresStore(os.Getenv("AGENSTRA_TEST_POSTGRES_DSN"), s.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := other.Close(); err != nil {
				t.Error(err)
			}
		}()
		s.Clock = func() float64 { return 1800000000.125 }
		other.Clock = s.Clock
		r := testRun(t, s, agentcontract.NewID())
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for _, store := range []*Store{s, other} {
			wg.Go(func() { _, e := store.Claim(r.RunID, "alice", 60); results <- e })
		}
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			} else if !errors.Is(err, ErrStoreConflict) {
				t.Fatal(err)
			}
		}
		if wins != 1 {
			t.Fatalf("independent claims won %d times", wins)
		}
		got, err := other.GetRun(r.RunID, "alice")
		if err != nil || got.CreatedAt != s.Now() {
			t.Fatalf("timestamp precision/reopen: %v %+v", err, got)
		}
	})
	t.Run("memory_and_schedules", func(t *testing.T) {
		s := testStore(t)
		m, err := s.SetMemory("alice", "records", agentcontract.MemoryUpdate{Scope: "pack", Key: "language", Value: "中文", Kind: "preference"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.GetMemory("bob", "records", m.ID); err == nil {
			t.Fatal("cross-owner memory read")
		}
		if _, err = s.ForgetMemory("alice", "records", m.ID, m.Revision); err != nil {
			t.Fatal(err)
		}
		history, err := s.MemoryHistory("alice", "records", m.ID)
		if err != nil || len(history.Revisions) != 1 || history.Revisions[0].Revision != 2 || history.Revisions[0].Value != "" {
			t.Fatalf("memory history: %v %+v", err, history)
		}
		now := 1800000000.125
		task := agentcontract.ScheduledTask{ScheduleID: agentcontract.NewID(), OwnerID: "alice", Status: "active", Revision: 1, CreatedAt: now, NextRunAt: &now, ScheduleRequest: agentcontract.ScheduleRequest{Name: "check", PackID: "records", Instruction: "read", Schedule: agentcontract.ScheduleSpec{Kind: "once", At: &now}}}
		if err = s.InsertSchedule(task); err != nil {
			t.Fatal(err)
		}
		if err = s.DispatchSchedule(task, agentcontract.NewID(), map[string]any{"test": true}, nil, nil, now); err != nil {
			t.Fatal(err)
		}
		executions, err := s.ScheduleExecutions(task.ScheduleID, "alice", 0, 10, false)
		if err != nil || len(executions) != 1 || executions[0].ScheduledAt != now {
			t.Fatalf("schedule execution: %v %+v", err, executions)
		}
	})
}
