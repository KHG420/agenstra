package service

import (
	"errors"
	"net/http"
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func (s *HTTPServer) workflowsHTTP(w http.ResponseWriter, r *http.Request, owner, pack string) {
	if r.URL.Path == "/workflows" && r.Method == "GET" {
		if pack == "" {
			pack = r.URL.Query().Get("pack_id")
		}
		catalog, err := s.Host.ListWorkflows(r.Context(), owner, pack)
		if err != nil {
			webError(w, err)
			return
		}
		writeJSON(w, 200, catalog)
		return
	}
	if r.URL.Path == "/workflows/runs" && r.Method == "POST" {
		var body struct {
			PackID    string             `json:"pack_id,omitempty"`
			Workflow  string             `json:"workflow"`
			Input     agentcontract.JSON `json:"input"`
			RequestID string             `json:"request_id"`
		}
		if decodeBodyLimit(r, &body, 512<<10) != nil {
			apiError(w, 422, "workflow_request_invalid", false)
			return
		}
		if pack == "" {
			pack = body.PackID
		} else if body.PackID != "" && body.PackID != pack {
			apiError(w, 403, "access_denied", false)
			return
		}
		run, err := s.Host.CreateWorkflow(r.Context(), owner, pack, body.Workflow, body.Input, body.RequestID)
		if err != nil {
			webError(w, err)
			return
		}
		view, err := s.Host.GetWorkflow(r.Context(), run.RunID, owner)
		if err != nil {
			webError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/workflows/runs/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/workflows/runs/") || len(parts) > 2 || !agentcontract.ValidUUID(parts[0]) {
		http.NotFound(w, r)
		return
	}
	view, err := s.Host.GetWorkflow(r.Context(), parts[0], owner)
	if err != nil {
		webError(w, err)
		return
	}
	if pack != "" {
		run, err := s.Host.Get(r.Context(), parts[0], owner)
		if err != nil {
			webError(w, err)
			return
		}
		if run.PackID != pack {
			apiError(w, 403, "access_denied", false)
			return
		}
	}
	if len(parts) == 1 && r.Method == "GET" {
		writeJSON(w, 200, view)
		return
	}
	if len(parts) != 2 || r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	switch parts[1] {
	case "cancel":
		_, err = s.Host.Cancel(r.Context(), parts[0], owner)
	case "resume":
		_, err = s.Host.ResumeWorkflow(r.Context(), parts[0], owner)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		webError(w, err)
		return
	}
	view, err = s.Host.GetWorkflow(r.Context(), parts[0], owner)
	if err != nil {
		webError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func (w *WebIntegration) workflowRunVisible(owner, id string) (bool, error) {
	run, err := w.Host.Store.GetRun(id, owner)
	if errors.Is(err, runstore.ErrRunNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	runtime, _ := run.State["runtime"].(map[string]any)
	if runtime["workflow"] == nil && runtime["workflow_parent"] == nil {
		return false, nil
	}
	for integration := range w.Config.Integrations {
		pack, err := w.integrationPack(integration)
		if err == nil && pack == run.PackID {
			return true, nil
		}
	}
	return false, nil
}
