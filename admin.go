package agenstra

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

//go:embed web/admin_ui.html web/admin_ui.css web/admin_ui.js web/admin_drafts.js web/admin_models.js
var adminAssets embed.FS

func registryHTTPError(w http.ResponseWriter, e error) {
	var re *RegistryError
	if errors.As(e, &re) {
		status := 422
		switch re.Code {
		case "release_not_found", "binding_not_found", "draft_not_found":
			status = 404
		case "revision_conflict", "version_already_published", "draft_revision_conflict", "draft_item_conflict", "model_revision_conflict":
			status = 409
		}
		apiError(w, status, re.Code, true)
		return
	}
	serverError(w, e)
}
func (s *HTTPServer) adminHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if s.Deployment.Registry == nil {
		http.NotFound(w, r)
		return
	}
	if p == "/admin" && r.Method == "GET" {
		adminHTML(w)
		return
	}
	if strings.HasPrefix(p, "/admin/assets/") && r.Method == "GET" {
		adminAsset(w, strings.TrimPrefix(p, "/admin/assets/"))
		return
	}
	if !strings.HasPrefix(p, "/admin/api/") {
		http.NotFound(w, r)
		return
	}
	key, e := s.Deployment.AdminKey()
	if e != nil {
		serverError(w, e)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(key)) != 1 {
		apiError(w, 401, "admin_unauthorized", true)
		return
	}
	registry := s.Deployment.Registry
	switch {
	case p == "/admin/api/models" || p == "/admin/api/models/check":
		s.adminModels(w, r)
	case p == "/admin/api/drafts" || strings.HasPrefix(p, "/admin/api/drafts/"):
		s.adminDraft(w, r)
	case p == "/admin/api/overview" && r.Method == "GET":
		releases, e := registry.ListPacks()
		if e != nil {
			registryHTTPError(w, e)
			return
		}
		bindings, e := registry.ListBindings()
		if e != nil {
			registryHTTPError(w, e)
			return
		}
		audit, e := registry.Audit(30)
		if e != nil {
			registryHTTPError(w, e)
			return
		}
		users := make([]string, 0, len(s.Deployment.Config.Users))
		for id := range s.Deployment.Config.Users {
			users = append(users, id)
		}
		sort.Strings(users)
		writeJSON(w, 200, map[string]any{"users": users, "releases": releases, "bindings": bindings, "audit": audit})
	case (p == "/admin/api/validate" || p == "/admin/api/releases") && r.Method == "POST":
		var b struct {
			PackID   string            `json:"pack_id"`
			Version  string            `json:"version"`
			Manifest map[string]any    `json:"manifest"`
			Skills   map[string]string `json:"skills"`
		}
		if decodeBody(r, &b) != nil || len(b.PackID) < 1 || len(b.PackID) > 128 || len(b.Version) < 1 || len(b.Version) > 40 || b.Manifest == nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		if b.Skills == nil {
			b.Skills = map[string]string{}
		}
		if p == "/admin/api/validate" {
			caps, e := registry.Validate(b.PackID, b.Version, b.Manifest, b.Skills)
			if e != nil {
				registryHTTPError(w, e)
				return
			}
			writeJSON(w, 200, map[string]any{"pack_id": b.PackID, "version": b.Version, "capabilities": caps})
		} else {
			result, e := registry.Publish(b.PackID, b.Version, b.Manifest, b.Skills)
			if e != nil {
				registryHTTPError(w, e)
				return
			}
			writeJSON(w, 200, result)
		}
	case p == "/admin/api/openapi-draft" && r.Method == "POST":
		var b struct {
			Spec       map[string]any    `json:"spec"`
			Name       string            `json:"name"`
			BaseURLEnv string            `json:"base_url_env"`
			TokenEnv   string            `json:"token_env"`
			Operations []string          `json:"operations"`
			Effects    map[string]string `json:"effects"`
		}
		if decodeBody(r, &b) != nil || len(b.Name) < 1 || len(b.Name) > 80 || b.Spec == nil || len(b.Operations) == 0 {
			apiError(w, 422, "invalid_request", true)
			return
		}
		specBytes, err := json.Marshal(b.Spec)
		if err != nil || len(specBytes) > 2000000 {
			apiError(w, 422, "openapi_import_failed", true)
			return
		}
		draft, e := ImportOpenAPIDocument(b.Spec, b.Name, b.BaseURLEnv, b.Operations, b.Effects, b.TokenEnv)
		if e != nil {
			writeJSON(w, 422, map[string]any{"detail": map[string]any{"code": "openapi_import_failed", "message": e.Error()}})
			return
		}
		writeJSON(w, 200, draft)
	case strings.HasPrefix(p, "/admin/api/packs/") && strings.HasSuffix(p, "/activate") && r.Method == "POST":
		pack := strings.TrimSuffix(strings.TrimPrefix(p, "/admin/api/packs/"), "/activate")
		pack = strings.TrimSuffix(pack, "/")
		var b struct {
			Digest           string `json:"digest"`
			ExpectedRevision *int   `json:"expected_revision"`
		}
		if decodeBody(r, &b) != nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		result, e := registry.Activate(pack, b.Digest, b.ExpectedRevision)
		if e != nil {
			registryHTTPError(w, e)
			return
		}
		writeJSON(w, 200, result)
	case strings.HasPrefix(p, "/admin/api/bindings/"):
		s.adminBinding(w, r, strings.TrimPrefix(p, "/admin/api/bindings/"))
	case p == "/admin/api/audit" && r.Method == "GET":
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil {
				registryHTTPError(w, registryError("invalid_limit"))
				return
			}
		}
		result, e := registry.Audit(limit)
		if e != nil {
			registryHTTPError(w, e)
			return
		}
		writeJSON(w, 200, result)
	default:
		http.NotFound(w, r)
	}
}
func adminHTML(w http.ResponseWriter) {
	b, e := adminAssets.ReadFile("web/admin_ui.html")
	if e != nil {
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if _, err := w.Write(b); err != nil {
		log.Print("HTTP response write failed")
	}
}
func adminAsset(w http.ResponseWriter, name string) {
	if name != "admin_ui.js" && name != "admin_ui.css" && name != "admin_drafts.js" && name != "admin_models.js" {
		http.NotFound(w, nil)
		return
	}
	b, e := adminAssets.ReadFile("web/" + name)
	if e != nil {
		w.WriteHeader(500)
		return
	}
	if strings.HasSuffix(name, ".js") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(b); err != nil {
		log.Print("HTTP response write failed")
	}
}
func (s *HTTPServer) adminBinding(w http.ResponseWriter, r *http.Request, path string) {
	check := strings.HasSuffix(path, "/check")
	if check {
		path = strings.TrimSuffix(path, "/check")
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	owner, pack := parts[0], parts[1]
	registry := s.Deployment.Registry
	switch {
	case check && r.Method == "POST":
		provider, e := s.Deployment.ProviderFactory(r.Context(), owner, pack)
		if e != nil {
			var dep *DeploymentError
			if errors.As(e, &dep) {
				apiError(w, 422, dep.Code, true)
			} else {
				apiError(w, 422, "connection_check_failed", true)
			}
			return
		}
		defer func() {
			// Closing the connection does not change an already observed business outcome.
			if err := provider.Close(); err != nil {
				log.Print("provider cleanup failed")
			}
		}()
		caps := make([]string, 0, len(provider.Capabilities()))
		for k := range provider.Capabilities() {
			caps = append(caps, k)
		}
		sort.Strings(caps)
		skills := make([]string, 0, len(provider.Skills()))
		for k := range provider.Skills() {
			skills = append(skills, k)
		}
		sort.Strings(skills)
		writeJSON(w, 200, map[string]any{"owner_id": owner, "pack_id": pack, "capabilities": caps, "skills": skills})
	case !check && r.Method == "PUT":
		if _, ok := s.Deployment.Config.Users[owner]; !ok && s.Deployment.Config.HostAuth == nil {
			apiError(w, 404, "owner_not_found", true)
			return
		}
		if s.Deployment.Config.HostAuth != nil && !validHostOwner(owner) {
			apiError(w, 422, "invalid_owner_id", true)
			return
		}
		var body ConnectionConfig
		if decodeBody(r, &body) != nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		body = normalizeConnection(body)
		if !validConnectionEnvironment(body, s.Deployment.Config.Management) {
			apiError(w, 422, "invalid_environment_ref", true)
			return
		}
		env := map[string]string{}
		for target, ref := range body.Environment {
			val, e := s.Deployment.Secret(ref)
			if e != nil {
				apiError(w, 422, "connection_unavailable", true)
				return
			}
			env[target] = val
		}
		if body.Identity != nil {
			for _, ref := range []string{body.Identity.URLEnv, body.Identity.TokenEnv} {
				if _, e := s.Deployment.Secret(ref); e != nil {
					apiError(w, 422, "connection_unavailable", true)
					return
				}
			}
		}
		active, e := registry.ActiveRelease(pack)
		if e != nil {
			registryHTTPError(w, e)
			return
		}
		if active == "" {
			registryHTTPError(w, registryError("release_not_active"))
			return
		}
		packPath, e := registry.ReleasePath(pack, active)
		if e != nil {
			registryHTTPError(w, e)
			return
		}
		if _, e = s.Deployment.BindingID(owner, pack, packPath, body, env); e != nil {
			var dep *DeploymentError
			if errors.As(e, &dep) {
				apiError(w, 422, dep.Code, true)
			} else {
				apiError(w, 422, "invalid_connection", true)
			}
			return
		}
		config, e := objectOf(body)
		if e != nil {
			registryHTTPError(w, e)
			return
		}
		if e = registry.PutBinding(owner, pack, config); e != nil {
			registryHTTPError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"owner_id": owner, "pack_id": pack, "enabled": true})
	case !check && r.Method == "DELETE":
		if e := registry.DisableBinding(owner, pack); e != nil {
			registryHTTPError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"owner_id": owner, "pack_id": pack, "enabled": false})
	default:
		w.WriteHeader(405)
	}
}
func validConnectionEnvironment(c ConnectionConfig, m *ManagementConfig) bool {
	for target, ref := range c.Environment {
		if !deploymentEnvName.MatchString(target) {
			return false
		}
		if deploymentEnvName.MatchString(ref) {
			continue
		}
		if m == nil || m.SecretDir == "" || !strings.HasPrefix(ref, "secret:") || !deploymentEnvName.MatchString(strings.TrimPrefix(ref, "secret:")) {
			return false
		}
	}
	return true
}
