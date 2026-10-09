package service

import (
	"encoding/json"
	"net/http"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func (s *HTTPServer) discoverDraftMCP(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		ExpectedRevision *int              `json:"expected_revision"`
		Environment      map[string]string `json:"environment"`
	}
	if decodeBody(r, &body) != nil || !validConnectionEnvironment(agentcontract.ConnectionConfig{Environment: body.Environment}, s.Deployment.Config.Management) {
		apiError(w, 422, "invalid_environment_ref", true)
		return
	}
	draft, err := s.Deployment.Registry.Draft(id)
	if err != nil {
		registryHTTPError(w, err)
		return
	}
	if body.ExpectedRevision == nil || *body.ExpectedRevision != draft.Revision {
		registryHTTPError(w, agentcontract.NewRegistryError("draft_revision_conflict"))
		return
	}
	if draft.Manifest["schema"] != "agenstra.mcp-pack.v1" {
		registryHTTPError(w, agentcontract.NewRegistryError("unsupported_pack_schema"))
		return
	}
	raw, err := json.Marshal(draft.Manifest["source"])
	if err != nil {
		apiError(w, 422, "mcp_discovery_source_invalid", true)
		return
	}
	var source agentcontract.MCPSource
	if jsonvalue.DecodeStrict(raw, &source) != nil {
		apiError(w, 422, "mcp_discovery_source_invalid", true)
		return
	}

	// Only resolve declared source variables. Values never enter the draft,
	// discovery response or error details.
	refs := []string{}
	for _, name := range []*string{source.URLEnv, source.TokenEnv, source.CWDEnv} {
		if name != nil {
			refs = append(refs, *name)
		}
	}
	for _, name := range source.Environment {
		refs = append(refs, name)
	}
	env := map[string]string{}
	for _, name := range refs {
		ref := body.Environment[name]
		if ref == "" {
			ref = name
		}
		if !validConnectionEnvironment(agentcontract.ConnectionConfig{Environment: map[string]string{name: ref}}, s.Deployment.Config.Management) {
			apiError(w, 422, "invalid_environment_ref", true)
			return
		}
		value, err := s.Deployment.Secret(ref)
		if err != nil {
			apiError(w, 422, "connection_unavailable", true)
			return
		}
		env[name] = value
	}
	tools, err := DiscoverMCPTools(r.Context(), source, env)
	if err != nil {
		apiError(w, 422, "mcp_discovery_failed", true)
		return
	}
	writeJSON(w, 200, map[string]any{"draft_id": id, "draft_revision": draft.Revision, "tools": tools})
}
