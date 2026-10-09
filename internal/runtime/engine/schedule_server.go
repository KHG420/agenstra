package engine

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *HTTPServer) schedulesHTTP(w http.ResponseWriter, r *http.Request, owner string) {
	ctx := r.Context()
	if r.URL.Path == "/schedules" {
		switch r.Method {
		case "POST":
			var request ScheduleRequest
			if decodeBody(r, &request) != nil {
				apiError(w, 422, "invalid_request", true)
				return
			}
			task, err := s.Host.CreateSchedule(ctx, owner, request)
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, 201, task)
		case "GET":
			limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "100"))
			if err != nil {
				apiError(w, 422, "invalid_limit", true)
				return
			}
			tasks, err := s.Host.ListSchedules(ctx, owner, limit)
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, 200, tasks)
		default:
			w.WriteHeader(405)
		}
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/schedules/"), "/")
	if len(parts) > 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case "GET":
			task, err := s.Host.GetSchedule(ctx, id, owner)
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, 200, task)
		case "PUT":
			var body struct {
				ScheduleRequest
				Revision *int `json:"revision"`
			}
			if decodeBody(r, &body) != nil || body.Revision == nil || *body.Revision < 0 {
				apiError(w, 422, "invalid_request", true)
				return
			}
			task, err := s.Host.UpdateSchedule(ctx, id, owner, *body.Revision, body.ScheduleRequest)
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, 200, task)
		case "DELETE":
			revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
			if err != nil || revision < 0 {
				apiError(w, 422, "invalid_request", true)
				return
			}
			if err = s.Host.DeleteSchedule(ctx, id, owner, revision); err != nil {
				serverError(w, err)
				return
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
		return
	}
	switch parts[1] {
	case "pause", "resume":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var body struct {
			Revision *int `json:"revision"`
		}
		if decodeBody(r, &body) != nil || body.Revision == nil || *body.Revision < 0 {
			apiError(w, 422, "invalid_request", true)
			return
		}
		var task ScheduledTask
		var err error
		if parts[1] == "pause" {
			task, err = s.Host.PauseSchedule(ctx, id, owner, *body.Revision)
		} else {
			task, err = s.Host.ResumeSchedule(ctx, id, owner, *body.Revision)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 200, task)
	case "executions":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		after, ea := strconv.ParseInt(defaultString(r.URL.Query().Get("after"), "0"), 10, 64)
		limit, el := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "100"))
		if ea != nil || el != nil {
			apiError(w, 422, "invalid_page", true)
			return
		}
		executions, err := s.Host.ListScheduleExecutions(ctx, id, owner, after, limit)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 200, executions)
	default:
		http.NotFound(w, r)
	}
}
