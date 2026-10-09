package engine

import (
	"net/http"
	"strconv"
	"strings"
)

// The same owner-scoped contract serves server credentials and short-lived Web
// tickets. Web callers select an integration; its pack comes from server config.
func (s *HTTPServer) memoriesHTTP(w http.ResponseWriter, r *http.Request, owner, fixedPack string) {
	w.Header().Set("Cache-Control", "no-store")
	pack := r.URL.Query().Get("pack_id")
	if fixedPack != "" {
		if pack != "" && pack != fixedPack {
			apiError(w, 422, "memory_invalid", false)
			return
		}
		pack = fixedPack
	}
	path := strings.TrimPrefix(r.URL.Path, "/memories")
	if path == "" {
		switch r.Method {
		case "GET":
			limit, e1 := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "100"))
			offset, e2 := strconv.Atoi(defaultString(r.URL.Query().Get("offset"), "0"))
			if pack == "" || e1 != nil || e2 != nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			items, err := s.Host.ListMemories(r.Context(), owner, pack, limit, offset)
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, 200, items)
		case "POST":
			var b struct {
				PackID   string `json:"pack_id"`
				Scope    string `json:"scope"`
				Key      string `json:"key"`
				Value    string `json:"value"`
				Kind     string `json:"kind"`
				Revision *int   `json:"revision"`
			}
			if decodeBody(r, &b) != nil || b.Revision == nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			if pack == "" {
				pack = b.PackID
			}
			if pack == "" || b.PackID != "" && b.PackID != pack {
				apiError(w, 422, "memory_invalid", false)
				return
			}
			m, err := s.Host.SetMemory(r.Context(), owner, pack, MemoryUpdate{Scope: b.Scope, Key: b.Key, Value: b.Value, Kind: b.Kind, Revision: *b.Revision})
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, 200, m)
		default:
			w.WriteHeader(405)
		}
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if pack == "" || len(parts) > 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 2 {
		if parts[1] != "history" {
			http.NotFound(w, r)
			return
		}
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		history, err := s.Host.MemoryHistory(r.Context(), owner, pack, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 200, history)
		return
	}
	switch r.Method {
	case "GET":
		m, err := s.Host.GetMemory(r.Context(), owner, pack, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 200, m)
	case "DELETE":
		var b struct {
			Revision *int `json:"revision"`
		}
		if decodeBody(r, &b) != nil || b.Revision == nil {
			apiError(w, 422, "invalid_request", false)
			return
		}
		m, err := s.Host.DeleteMemory(r.Context(), owner, pack, id, *b.Revision)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 200, m)
	default:
		w.WriteHeader(405)
	}
}
