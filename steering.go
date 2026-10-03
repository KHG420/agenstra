package agenstra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var errSteeringPending = errors.New("steering pending at completion checkpoint")

type steeringMessage struct {
	Sequence  int
	RequestID string
	Text      string
}

func steeringCursor(envelope JSON) int {
	runtime, _ := envelope["runtime"].(map[string]any)
	cursor, _ := pathIndex(runtime["steering_cursor"])
	return cursor
}

func latestSteering(q sqlQueryer, id string) (int, error) {
	var sequence int
	err := q.QueryRow("SELECT coalesce(max(sequence),0) FROM events WHERE run_id=? AND event_json->>'kind'='steering_requested'", id).Scan(&sequence)
	return sequence, err
}

// Requests live in the existing event journal, separate from the driver's
// checkpoint. Appending one cannot overwrite an active worker's state.
func (s *SQLiteStore) queueSteering(id, owner, requestID, text string, revision int) (run StoredRun, err error) {
	err = s.write(func(tx *sql.Tx) error {
		var e error
		run, e = owned(tx, id, owner)
		if e != nil {
			return e
		}
		var previous string
		e = tx.QueryRow("SELECT event_json->>'text' FROM events WHERE run_id=? AND event_json->>'kind'='steering_requested' AND event_json->>'request_id'=?", id, requestID).Scan(&previous)
		if e == nil {
			if previous != text {
				return hostError("steering_request_conflict")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if run.Revision != revision {
			return hostError("revision_conflict")
		}
		if run.CancelRequested || (run.Status != "queued" && run.Status != "running" && run.Status != "waiting" && run.Status != "needs_approval") {
			return hostError("steering_not_available")
		}
		var pending int
		if e = tx.QueryRow("SELECT count(*) FROM events WHERE run_id=? AND sequence>? AND event_json->>'kind'='steering_requested'", id, steeringCursor(run.State)).Scan(&pending); e != nil {
			return e
		}
		if pending >= 32 {
			return hostError("steering_queue_full")
		}
		payload, e := CanonicalJSON(JSON{"kind": "steering_requested", "request_id": requestID, "text": text})
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO events(run_id,created_at,event_json) VALUES(?,?,?)", id, s.now(), string(payload)); e != nil {
			return e
		}
		if run.LeaseUntil == nil || *run.LeaseUntil <= s.now() {
			if _, e = tx.Exec("UPDATE runs SET status='queued',next_wake_at=?,updated_at=? WHERE run_id=?", s.now(), s.now(), id); e != nil {
				return e
			}
		}
		run, e = owned(tx, id, owner)
		return e
	})
	return
}

func (s *SQLiteStore) pendingSteering(id, owner string, after int) (result []steeringMessage, resultErr error) {
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

// Steer appends authorized user input under a stable request identity and expected run revision.
func (h *AgentHost) Steer(ctx context.Context, id, owner, requestID, text string, revision int) (StoredRun, error) {
	if _, err := h.Get(ctx, id, owner); err != nil {
		return StoredRun{}, err
	}
	if !validUUID(requestID) || revision < 0 || strings.TrimSpace(text) == "" || len([]rune(text)) > 30000 {
		return StoredRun{}, hostError("steering_invalid")
	}
	return h.Store.queueSteering(id, owner, requestID, text, revision)
}

func (h *AgentHost) applySteering(run StoredRun, state *RuntimeState) (StoredRun, bool, error) {
	for _, item := range state.Pending {
		if item.Status == "in_flight" || item.Status == "unknown" || item.PollInFlight {
			return run, false, nil
		}
	}
	messages, err := h.Store.pendingSteering(run.RunID, run.OwnerID, state.SteeringCursor)
	if err != nil || len(messages) == 0 {
		return run, false, err
	}
	var inputs []memoryInput
	if run.State["memory_inputs"] != nil {
		raw, err := CanonicalJSON(run.State["memory_inputs"])
		if err != nil {
			return run, false, hostError("run_state_invalid")
		}
		if err = strictUnmarshal(raw, &inputs); err != nil {
			return run, false, hostError("run_state_invalid")
		}
	}
	for _, message := range messages {
		state.Followups = append(state.Followups, "steering: "+message.Text)
		state.SteeringCursor = message.Sequence
		inputs = append(inputs, memoryInput{ID: fmt.Sprintf("%s:steering:%s", run.RunID, message.RequestID), Text: message.Text})
	}
	run.State["memory_inputs"] = inputs
	// Only unsent calls are superseded. Settled evidence and submitted jobs remain.
	for i := range state.Pending {
		item := &state.Pending[i]
		if item.Attempts == 0 && (item.Status == "prepared" || item.Status == "needs_approval") {
			Observe(state, item, CallOutcome{ErrorCode: "steering_superseded"})
			item.ApprovedHash, item.ApprovedUntil = nil, nil
		}
	}
	state.Status = "running"
	state.AnswerMarkdown = ""
	state.InputField, state.InputPrompt, state.ErrorCode = nil, nil, nil
	run, err = h.save(run, state, "", nil, JSON{"kind": "steering_applied", "through_sequence": state.SteeringCursor, "count": len(messages)})
	return run, true, err
}
