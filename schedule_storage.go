package agenstra

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Schedules extend the database without changing the v1 runs schema or version.
// Existing Python/Go runs and checkpoints remain readable.
func initializeScheduleTables(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS scheduled_tasks (
	 schedule_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, status TEXT NOT NULL,
	 next_run_at REAL, revision INTEGER NOT NULL, created_at REAL NOT NULL, payload_json TEXT NOT NULL);
	 CREATE INDEX IF NOT EXISTS scheduled_tasks_due ON scheduled_tasks(status,next_run_at);
	 CREATE INDEX IF NOT EXISTS scheduled_tasks_owner ON scheduled_tasks(owner_id,created_at);
	 CREATE TABLE IF NOT EXISTS schedule_executions (
	 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	 schedule_id TEXT NOT NULL REFERENCES scheduled_tasks(schedule_id) ON DELETE CASCADE,
	 owner_id TEXT NOT NULL, scheduled_at REAL NOT NULL, triggered_at REAL NOT NULL,
	 run_id TEXT REFERENCES runs(run_id), status TEXT NOT NULL, error_code TEXT NOT NULL);
	 CREATE INDEX IF NOT EXISTS schedule_executions_task ON schedule_executions(schedule_id,sequence);
	 CREATE INDEX IF NOT EXISTS schedule_executions_run ON schedule_executions(run_id);`)
	return err
}

func ownedSchedule(q sqlQueryer, id, owner string) (ScheduledTask, error) {
	var task ScheduledTask
	var raw string
	err := q.QueryRow("SELECT payload_json FROM scheduled_tasks WHERE schedule_id=? AND owner_id=?", id, owner).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return task, hostError("not_found")
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &task)
	}
	return task, err
}
func saveSchedule(tx *sql.Tx, task ScheduledTask) error {
	task.LastExecution = nil // Current run state is always read, never cached.
	raw, err := CanonicalJSON(task)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE scheduled_tasks SET status=?,next_run_at=?,revision=?,payload_json=? WHERE schedule_id=? AND owner_id=?", task.Status, task.NextRunAt, task.Revision, string(raw), task.ScheduleID, task.OwnerID)
	return err
}
func (s *SQLiteStore) insertSchedule(task ScheduledTask) error {
	raw, err := CanonicalJSON(task)
	if err != nil {
		return err
	}
	return s.write(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO scheduled_tasks(schedule_id,owner_id,status,next_run_at,revision,created_at,payload_json) VALUES(?,?,?,?,?,?,?)", task.ScheduleID, task.OwnerID, task.Status, task.NextRunAt, task.Revision, task.CreatedAt, string(raw))
		return err
	})
}
func (s *SQLiteStore) getSchedule(id, owner string) (ScheduledTask, error) {
	task, err := ownedSchedule(s.DB, id, owner)
	if err != nil {
		return task, err
	}
	last, err := s.scheduleExecutions(id, owner, 0, 1, true)
	if err == nil && len(last) > 0 {
		task.LastExecution = &last[0]
	}
	return task, err
}
func (s *SQLiteStore) querySchedules(query string, args ...any) ([]ScheduledTask, error) {
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ScheduledTask{}
	for rows.Next() {
		var raw string
		var task ScheduledTask
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &task); err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, rows.Err()
}
func (s *SQLiteStore) listSchedules(owner string, limit int) ([]ScheduledTask, error) {
	tasks, err := s.querySchedules("SELECT payload_json FROM scheduled_tasks WHERE owner_id=? ORDER BY created_at DESC,schedule_id LIMIT ?", owner, limit)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		last, err := s.scheduleExecutions(tasks[i].ScheduleID, owner, 0, 1, true)
		if err != nil {
			return nil, err
		}
		if len(last) > 0 {
			tasks[i].LastExecution = &last[0]
		}
	}
	return tasks, nil
}
func (s *SQLiteStore) dueSchedules(now float64, limit int) ([]ScheduledTask, error) {
	return s.querySchedules("SELECT payload_json FROM scheduled_tasks WHERE status='active' AND next_run_at<=? ORDER BY next_run_at,schedule_id LIMIT ?", now, limit)
}
func (s *SQLiteStore) replaceSchedule(task ScheduledTask, revision int, now float64) (ScheduledTask, error) {
	err := s.write(func(tx *sql.Tx) error {
		current, err := ownedSchedule(tx, task.ScheduleID, task.OwnerID)
		if err != nil {
			return err
		}
		// Also compare the revision read by the caller: supplying a newer
		// revision must not overwrite a definition read before another edit.
		if revision < 0 || current.Revision != revision || task.Revision != revision {
			return hostError("revision_conflict")
		}
		task.Revision++
		task.UpdatedAt = now
		return saveSchedule(tx, task)
	})
	return task, err
}
func (s *SQLiteStore) deleteSchedule(id, owner string, revision int) error {
	return s.write(func(tx *sql.Tx) error {
		task, err := ownedSchedule(tx, id, owner)
		if err != nil {
			return err
		}
		if revision < 0 || task.Revision != revision {
			return hostError("revision_conflict")
		}
		_, err = tx.Exec("DELETE FROM scheduled_tasks WHERE schedule_id=? AND owner_id=?", id, owner)
		return err
	})
}

func (s *SQLiteStore) dispatchSchedule(task ScheduledTask, runID string, state map[string]any, prepareErr error, next *float64, now float64) error {
	return s.write(func(tx *sql.Tx) error {
		current, err := ownedSchedule(tx, task.ScheduleID, task.OwnerID)
		if err != nil {
			return err
		}
		if current.Revision != task.Revision || current.Status != "active" || current.NextRunAt == nil || *current.NextRunAt > now {
			return ErrStoreConflict
		}
		status, errorCode := "queued", ""
		var storedRunID any
		if prepareErr != nil {
			status, errorCode = "needs_authorization", ErrorCode(prepareErr)
			task.Status = "paused"
		} else {
			var active int
			if err = tx.QueryRow(`SELECT count(*) FROM schedule_executions e JOIN runs r ON r.run_id=e.run_id
			 WHERE e.schedule_id=? AND r.status NOT IN ('completed','failed','cancelled')`, task.ScheduleID).Scan(&active); err != nil {
				return err
			}
			if active > 0 {
				status = "skipped_overlap"
			} else {
				raw, err := CanonicalJSON(state)
				if err != nil {
					return err
				}
				if _, err = tx.Exec("INSERT INTO runs(run_id,owner_id,pack_id,status,state_json,revision,created_at,updated_at) VALUES(?,?,?,'queued',?,0,?,?)", runID, task.OwnerID, task.PackID, string(raw), now, now); err != nil {
					return err
				}
				event, err := CanonicalJSON(map[string]any{"kind": "schedule_triggered", "schedule_id": task.ScheduleID, "scheduled_at": *task.NextRunAt})
				if err != nil {
					return err
				}
				if _, err = tx.Exec("INSERT INTO events(run_id,created_at,event_json) VALUES(?,?,?)", runID, now, string(event)); err != nil {
					return err
				}
				storedRunID = runID
			}
			task.NextRunAt = next
			if next == nil {
				task.Status = "completed"
			}
		}
		if _, err = tx.Exec("INSERT INTO schedule_executions(schedule_id,owner_id,scheduled_at,triggered_at,run_id,status,error_code) VALUES(?,?,?,?,?,?,?)", task.ScheduleID, task.OwnerID, *current.NextRunAt, now, storedRunID, status, errorCode); err != nil {
			return err
		}
		task.Revision++
		task.UpdatedAt = now
		return saveSchedule(tx, task)
	})
}

func (s *SQLiteStore) scheduleExecutions(id, owner string, after int64, limit int, latest bool) ([]ScheduleExecution, error) {
	order := "ASC"
	if latest {
		order = "DESC"
	}
	rows, err := s.DB.Query(`SELECT e.sequence,e.schedule_id,e.scheduled_at,e.triggered_at,e.run_id,e.status,e.error_code,r.status,r.state_json
	 FROM schedule_executions e LEFT JOIN runs r ON r.run_id=e.run_id AND r.owner_id=e.owner_id
	 WHERE e.schedule_id=? AND e.owner_id=? AND e.sequence>? ORDER BY e.sequence `+order+` LIMIT ?`, id, owner, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ScheduleExecution{}
	for rows.Next() {
		var execution ScheduleExecution
		var runID, status, state sql.NullString
		if err = rows.Scan(&execution.Sequence, &execution.ScheduleID, &execution.ScheduledAt, &execution.TriggeredAt, &runID, &execution.Status, &execution.ErrorCode, &status, &state); err != nil {
			return nil, err
		}
		execution.RunID = runID.String
		if status.Valid {
			execution.Status = status.String
			var envelope struct {
				Runtime struct {
					ErrorCode *string `json:"error_code"`
				} `json:"runtime"`
			}
			if err = json.Unmarshal([]byte(state.String), &envelope); err != nil {
				return nil, err
			}
			if envelope.Runtime.ErrorCode != nil {
				execution.ErrorCode = *envelope.Runtime.ErrorCode
			}
		}
		result = append(result, execution)
	}
	return result, rows.Err()
}
