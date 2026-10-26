package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// Connections are short lived so ticket renewal and reconnect use the normal
// authentication path. Every snapshot rechecks the owner's current run policy.
func (s *HTTPServer) answerHTTP(w http.ResponseWriter, r *http.Request, owner string, ticketValid func() bool) {
	id := strings.Split(strings.TrimPrefix(r.URL.Path, "/runs/"), "/")[0]
	update, err := s.Host.AnswerSnapshot(r.Context(), id, owner)
	if err != nil {
		serverError(w, err)
		return
	}
	controller := http.NewResponseController(w)
	defer func() {
		// Do not leave this response's slow-reader deadline on a reused connection.
		if err := controller.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			log.Print("answer stream deadline cleanup failed")
		}
	}()
	write := func(value agentcontract.AnswerUpdate) bool {
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "event: answer\ndata: %s\n\n", raw); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if !write(update) {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	for update.Phase != "completed" && update.Phase != "failed" && update.Phase != "cancelled" {
		select {
		case <-r.Context().Done():
			return
		case <-s.stop:
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			if ticketValid != nil && !ticketValid() {
				write(agentcontract.AnswerUpdate{RunID: id, Phase: "waiting"})
				return
			}
			next, err := s.Host.AnswerSnapshot(r.Context(), id, owner)
			if err != nil {
				write(agentcontract.AnswerUpdate{RunID: id, Phase: "failed"})
				return
			}
			if next != update {
				if !write(next) {
					return
				}
				update = next
			}
		}
	}
}
