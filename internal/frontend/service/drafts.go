package service

import (
	"net/http"
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	packregistry "github.com/KHG420/agenstra/internal/state/registry"
)

func (s *HTTPServer) adminDraft(w http.ResponseWriter, r *http.Request) {
	registry := s.Deployment.Registry
	path := strings.TrimPrefix(r.URL.Path, "/admin/api/drafts")
	if path == "" && r.Method == "GET" {
		result, err := registry.ListDrafts()
		if err != nil {
			registryHTTPError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) > 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 2 {
		if parts[1] == "discover" && r.Method == "POST" {
			s.discoverDraftMCP(w, r, id)
			return
		}
		if r.Method != "POST" || (parts[1] != "validate" && parts[1] != "publish") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			ExpectedRevision *int `json:"expected_revision"`
		}
		if decodeBody(r, &body) != nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		d, err := registry.Draft(id)
		if err != nil {
			registryHTTPError(w, err)
			return
		}
		if body.ExpectedRevision == nil || *body.ExpectedRevision != d.Revision {
			registryHTTPError(w, agentcontract.NewRegistryError("draft_revision_conflict"))
			return
		}
		if parts[1] == "validate" {
			writeJSON(w, 200, d)
			return
		}
		if len(d.Issues) > 0 {
			writeJSON(w, 422, map[string]any{"detail": map[string]any{"code": "draft_incomplete", "issues": d.Issues}})
			return
		}
		result, err := registry.Publish(packregistry.DraftString(d.Manifest, "name"), packregistry.DraftString(d.Manifest, "version"), d.Manifest, d.Skills)
		if err != nil {
			registryHTTPError(w, err)
			return
		}
		result["draft_revision"] = d.Revision
		writeJSON(w, 200, result)
		return
	}
	switch r.Method {
	case "GET":
		d, err := registry.Draft(id)
		if err != nil {
			registryHTTPError(w, err)
			return
		}
		writeJSON(w, 200, d)
	case "PUT":
		var b struct {
			ExpectedRevision *int              `json:"expected_revision"`
			Manifest         map[string]any    `json:"manifest"`
			Skills           map[string]string `json:"skills"`
		}
		if decodeBody(r, &b) != nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		d, err := registry.SaveDraft(id, b.ExpectedRevision, b.Manifest, b.Skills)
		if err != nil {
			registryHTTPError(w, err)
			return
		}
		writeJSON(w, 200, d)
	case "PATCH":
		var edit agentcontract.DraftEdit
		if decodeBody(r, &edit) != nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		d, err := registry.EditDraft(id, edit)
		if err != nil {
			registryHTTPError(w, err)
			return
		}
		writeJSON(w, 200, d)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
