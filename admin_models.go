package agenstra

import "net/http"

func (s *HTTPServer) adminModels(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.Host.Model.(*ModelManager)
	if r.URL.Path == "/admin/api/models" && r.Method == "GET" {
		if !ok {
			writeJSON(w, 200, JSON{"available": false})
			return
		}
		snapshot := manager.Snapshot()
		writeJSON(w, 200, JSON{"available": true, "revision": snapshot.Revision, "config": snapshot.Config})
		return
	}
	if !ok {
		registryHTTPError(w, registryError("model_management_unavailable"))
		return
	}
	switch {
	case r.URL.Path == "/admin/api/models" && r.Method == "PUT":
		var body struct {
			ExpectedRevision *int               `json:"expected_revision"`
			Config           ModelConfiguration `json:"config"`
		}
		if decodeBody(r, &body) != nil || body.ExpectedRevision == nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		snapshot, err := manager.Configure(body.Config, *body.ExpectedRevision)
		if err != nil {
			registryHTTPError(w, err)
			return
		}
		writeJSON(w, 200, snapshot)
	case r.URL.Path == "/admin/api/models/check" && r.Method == "POST":
		var body struct {
			Profile string              `json:"profile"`
			Purpose string              `json:"purpose"`
			Config  *ModelConfiguration `json:"config,omitempty"`
		}
		if decodeBody(r, &body) != nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		result, err := manager.Check(r.Context(), body.Profile, body.Purpose, body.Config)
		if err != nil {
			registryHTTPError(w, err)
			return
		}
		writeJSON(w, 200, result)
	default:
		http.NotFound(w, r)
	}
}
