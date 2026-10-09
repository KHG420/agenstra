package runstore

import (
	"database/sql"
	"errors"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

var ErrSteeringPending = errors.New("steering pending at completion checkpoint")

type steeringMessage struct {
	Sequence  int
	RequestID string
	Text      string
}

func steeringCursor(envelope agentcontract.JSON) int {
	runtime, _ := envelope["runtime"].(map[string]any)
	cursor, _ := jsonvalue.Index(runtime["steering_cursor"])
	return cursor
}

func latestSteering(q SQLQueryer, id string) (int, error) {
	var sequence int
	err := q.QueryRow("SELECT coalesce(max(sequence),0) FROM events WHERE run_id=? AND event_json->>'kind'='steering_requested'", id).Scan(&sequence)
	return sequence, err
}

// QueueSteering atomically validates the run revision and accepts idempotent steering text.
// Requests live in the existing event journal, separate from the driver's
// checkpoint. Appending one cannot overwrite an active worker's state.
func (s *SQLiteStore) QueueSteering(id, owner, requestID, text string, revision int) (run agentcontract.StoredRun, err error) {
	err = s.Transaction(func(tx *sql.Tx) error {
		var e error
		run, e = Owned(tx, id, owner)
		if e != nil {
			return e
		}
		var previous string
		e = tx.QueryRow("SELECT event_json->>'text' FROM events WHERE run_id=? AND event_json->>'kind'='steering_requested' AND event_json->>'request_id'=?", id, requestID).Scan(&previous)
		if e == nil {
			if previous != text {
				return agentcontract.NewHostError("steering_request_conflict")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if run.Revision != revision {
			return agentcontract.NewHostError("revision_conflict")
		}
		if run.CancelRequested || (run.Status != "queued" && run.Status != "running" && run.Status != "waiting" && run.Status != "needs_approval") {
			return agentcontract.NewHostError("steering_not_available")
		}
		var pending int
		if e = tx.QueryRow("SELECT count(*) FROM events WHERE run_id=? AND sequence>? AND event_json->>'kind'='steering_requested'", id, steeringCursor(run.State)).Scan(&pending); e != nil {
			return e
		}
		if pending >= 32 {
			return agentcontract.NewHostError("steering_queue_full")
		}
		payload, e := agentcontract.CanonicalJSON(agentcontract.JSON{"kind": "steering_requested", "request_id": requestID, "text": text})
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO events(run_id,created_at,event_json) VALUES(?,?,?)", id, s.Now(), string(payload)); e != nil {
			return e
		}
		if run.LeaseUntil == nil || *run.LeaseUntil <= s.Now() {
			if _, e = tx.Exec("UPDATE runs SET status='queued',next_wake_at=?,updated_at=? WHERE run_id=?", s.Now(), s.Now(), id); e != nil {
				return e
			}
		}
		run, e = Owned(tx, id, owner)
		return e
	})
	return
}

// PendingSteering returns the run's saved steering messages after the consumed sequence.
func (s *SQLiteStore) PendingSteering(id, owner string, after int) (result []steeringMessage, resultErr error) {
	if _, err := s.GetRun(id, owner); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query("SELECT sequence,event_json->>'request_id',event_json->>'text' FROM events WHERE run_id=? AND sequence>? AND event_json->>'kind'='steering_requested' ORDER BY sequence LIMIT 32", id, after)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	var messages []steeringMessage
	for rows.Next() {
		var message steeringMessage
		if err = rows.Scan(&message.Sequence, &message.RequestID, &message.Text); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}
