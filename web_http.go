package agenstra

import (
	"embed"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed web/agenstra-client.js
var webAssets embed.FS

func webError(w http.ResponseWriter, e error) {
	var h *HostError
	var d *DeploymentError
	if !errors.As(e, &h) && !errors.As(e, &d) {
		serverError(w, e)
		return
	}
	code := ErrorCode(e)
	status := 409
	switch code {
	case "not_found":
		status = 404
	case "unauthorized", "browser_session_invalid":
		status = 401
	case "access_denied", "identity_unverified", "model_data_not_authorized", "capability_not_granted":
		status = 403
	case "source_scope_invalid", "chat_message_invalid", "browser_context_invalid", "browser_result_invalid", "browser_handler_unknown", "request_id_required":
		status = 422
	}
	apiError(w, status, code, false)
}
func webBearer(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(value, "Bearer ")
}
func (s *HTTPServer) webHTTP(w http.ResponseWriter, r *http.Request) {
	integration := s.Web
	w.Header().Set("Cache-Control", "no-store")
	origin := r.Header.Get("Origin")
	if origin != "" {
		u, e := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		allowed := e == nil && u.Scheme == scheme && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
		for _, o := range integration.Config.AllowedOrigins {
			if o == origin {
				allowed = true
			}
		}
		if !allowed {
			apiError(w, 403, "origin_not_allowed", false)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Agenstra-Browser-Key")
			w.WriteHeader(204)
			return
		}
	}
	p := r.URL.Path
	if strings.HasPrefix(p, "/web/assets/") && r.Method == "GET" {
		name := strings.TrimPrefix(p, "/web/assets/")
		if name != "agenstra-client.js" {
			http.NotFound(w, r)
			return
		}
		raw, e := webAssets.ReadFile("web/" + name)
		if e != nil {
			serverError(w, e)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Write(raw)
		return
	}
	if p == "/web/v1/token" {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var owner string
		var e error
		if integration.AuthenticateRequest != nil {
			owner, e = integration.AuthenticateRequest(r)
		} else {
			owner, e = s.owner(r)
		}
		if e != nil || owner == "" {
			apiError(w, 401, "unauthorized", false)
			return
		}
		token, e := integration.MintSession(owner)
		if e != nil {
			webError(w, e)
			return
		}
		writeJSON(w, 200, JSON{"token": token, "expires_at": time.Now().Unix() + int64(integration.Config.SessionTTLSeconds)})
		return
	}
	owner, e := integration.ticketOwner(webBearer(r))
	if e != nil {
		apiError(w, 401, "unauthorized", false)
		return
	}
	if p == "/web/v1/memories" || strings.HasPrefix(p, "/web/v1/memories/") {
		pack, err := integration.integrationPack(r.URL.Query().Get("integration_id"))
		if err != nil {
			webError(w, err)
			return
		}
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Path = strings.TrimPrefix(p, "/web/v1")
		copy.URL = &u
		s.memoriesHTTP(w, copy, owner, pack)
		return
	}
	if strings.HasPrefix(p, "/web/v1/runs/") {
		path := strings.TrimPrefix(p, "/web/v1")
		id := strings.Split(strings.TrimPrefix(path, "/runs/"), "/")[0]
		var count int
		e = integration.Store.store.DB.QueryRow("SELECT (SELECT count(*) FROM web_bindings WHERE run=? AND owner=?)+(SELECT count(*) FROM web_messages WHERE payload->>'run_id'=? AND owner=?)", id, owner, id, owner).Scan(&count)
		if e != nil {
			serverError(w, e)
			return
		}
		if count == 0 {
			http.NotFound(w, r)
			return
		}
		if r.Method == "POST" && strings.HasSuffix(path, "/cancel") {
			if e = integration.cancelCommands(owner, id); e != nil {
				serverError(w, e)
				return
			}
		}
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Path = path
		copy.URL = &u
		s.runHTTP(w, copy, owner)
		return
	}
	if strings.HasPrefix(p, "/chat/v1/") {
		if !integration.Config.Chat {
			http.NotFound(w, r)
			return
		}
		s.chatHTTP(w, r, owner)
		return
	}
	if strings.HasPrefix(p, "/browser/v1/") {
		if !integration.Config.BrowserBridge {
			http.NotFound(w, r)
			return
		}
		s.browserHTTP(w, r, owner)
		return
	}
	http.NotFound(w, r)
}
func (s *HTTPServer) chatHTTP(w http.ResponseWriter, r *http.Request, owner string) {
	p := strings.TrimPrefix(r.URL.Path, "/chat/v1/")
	if p == "conversations" {
		if r.Method == "GET" {
			items, e := s.Web.ListConversations(r.Context(), owner, r.URL.Query().Get("integration_id"))
			if e != nil {
				webError(w, e)
				return
			}
			writeJSON(w, 200, items)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var body struct {
			IntegrationID string `json:"integration_id"`
		}
		if decodeBody(r, &body) != nil {
			apiError(w, 422, "invalid_request", false)
			return
		}
		c, e := s.Web.CreateConversation(r.Context(), owner, body.IntegrationID)
		if e != nil {
			webError(w, e)
			return
		}
		writeJSON(w, 200, c)
		return
	}
	parts := strings.Split(p, "/")
	if len(parts) == 2 && parts[0] == "conversations" && r.Method == "GET" {
		c, m, e := s.Web.Conversation(r.Context(), owner, parts[1])
		if e != nil {
			webError(w, e)
			return
		}
		writeJSON(w, 200, JSON{"conversation": c, "messages": m})
		return
	}
	if len(parts) == 3 && parts[0] == "conversations" && parts[2] == "messages" && r.Method == "POST" {
		var b struct {
			Sources   []RunSource `json:"sources,omitempty"`
			ClientID  string      `json:"client_id"`
			Text      string      `json:"text"`
			SessionID string      `json:"session_id"`
		}
		if decodeBody(r, &b) != nil {
			apiError(w, 422, "invalid_request", false)
			return
		}
		if s.Web.profilesForConversation(owner, parts[1]) && !s.Web.sessionKeyMatches(owner, b.SessionID, r.Header.Get("X-Agenstra-Browser-Key")) {
			apiError(w, 401, "browser_session_invalid", false)
			return
		}
		m, e := s.Web.SubmitMessageWithSources(r.Context(), owner, parts[1], b.ClientID, b.Text, b.SessionID, b.Sources)
		if e != nil {
			webError(w, e)
			return
		}
		m.Instruction = ""
		writeJSON(w, 200, m)
		return
	}
	if len(parts) == 3 && parts[0] == "messages" && parts[2] == "cancel" && r.Method == "POST" {
		m, e := s.Web.CancelMessage(r.Context(), owner, parts[1])
		if e != nil {
			webError(w, e)
			return
		}
		m.Instruction = ""
		writeJSON(w, 200, m)
		return
	}
	http.NotFound(w, r)
}
func (w *WebIntegration) profilesForConversation(owner, id string) bool {
	var c ChatConversation
	if webLoad(w.Store.store.DB, "web_conversations", id, owner, &c) != nil {
		return false
	}
	return w.profiles[c.IntegrationID] != nil
}
func (w *WebIntegration) sessionKeyMatches(owner, id, key string) bool {
	var b BrowserSession
	if webLoad(w.Store.store.DB, "web_sessions", id, owner, &b) != nil {
		return false
	}
	return key != "" && webHash(key) == b.KeyHash
}
func (s *HTTPServer) browserHTTP(w http.ResponseWriter, r *http.Request, owner string) {
	p := strings.TrimPrefix(r.URL.Path, "/browser/v1/")
	parts := strings.Split(p, "/")
	key := r.Header.Get("X-Agenstra-Browser-Key")
	if len(parts) == 2 && parts[0] == "integrations" && r.Method == "GET" {
		profile := s.Web.profiles[parts[1]]
		if profile == nil {
			http.NotFound(w, r)
			return
		}
		if _, e := s.Host.policy(r.Context(), owner, parts[1], true); e != nil {
			webError(w, e)
			return
		}
		writeJSON(w, 200, JSON{"profile": profile.profile, "digest": profile.digest})
		return
	}
	if p == "sessions" && r.Method == "POST" {
		var b struct {
			IntegrationID  string   `json:"integration_id"`
			HandlerVersion string   `json:"handler_version"`
			Handlers       []string `json:"handlers"`
		}
		if decodeBody(r, &b) != nil {
			apiError(w, 422, "invalid_request", false)
			return
		}
		session, key, e := s.Web.CreateBrowserSession(r.Context(), owner, b.IntegrationID, b.HandlerVersion, b.Handlers)
		if e != nil {
			webError(w, e)
			return
		}
		writeJSON(w, 200, JSON{"session": session, "key": key})
		return
	}
	if p == "runs" && r.Method == "POST" {
		var b struct {
			IntegrationID string      `json:"integration_id"`
			SessionID     string      `json:"session_id"`
			Instruction   string      `json:"instruction"`
			RequestID     string      `json:"request_id"`
			Sources       []RunSource `json:"sources,omitempty"`
		}
		if decodeBody(r, &b) != nil {
			apiError(w, 422, "invalid_request", false)
			return
		}
		if !s.Web.sessionKeyMatches(owner, b.SessionID, key) {
			apiError(w, 401, "browser_session_invalid", false)
			return
		}
		run, e := s.Web.CreateBrowserRunWithSources(r.Context(), owner, b.IntegrationID, b.SessionID, b.Instruction, b.RequestID, b.Sources)
		if e != nil {
			webError(w, e)
			return
		}
		writeJSON(w, 200, runView(run, s.Host))
		return
	}
	if len(parts) == 3 && parts[0] == "sessions" && r.Method == "POST" {
		switch parts[2] {
		case "recover":
			var b struct {
				Generation         int      `json:"generation"`
				HandlerVersion     string   `json:"handler_version"`
				Handlers           []string `json:"handlers"`
				RequestID          string   `json:"request_id"`
				AcknowledgeUnknown bool     `json:"acknowledge_unknown"`
			}
			if decodeBody(r, &b) != nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			session, newKey, e := s.Web.RecoverBrowserSession(r.Context(), owner, parts[1], key, b.Generation, b.HandlerVersion, b.Handlers, b.RequestID, b.AcknowledgeUnknown)
			if e != nil {
				webError(w, e)
				return
			}
			writeJSON(w, 200, JSON{"session": session, "key": newKey})
			return
		case "resume":
			var b struct {
				Generation int `json:"generation"`
			}
			if decodeBody(r, &b) != nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			session, e := s.Web.ResumeBrowserSession(owner, parts[1], key, b.Generation)
			if e != nil {
				webError(w, e)
				return
			}
			writeJSON(w, 200, JSON{"session": session})
			return
		case "observation":
			var b struct {
				Generation  int  `json:"generation"`
				Revision    int  `json:"revision"`
				Observation JSON `json:"observation"`
			}
			if decodeBody(r, &b) != nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			session, e := s.Web.UpdatePageObservation(owner, parts[1], key, b.Generation, b.Revision, b.Observation)
			if e != nil {
				webError(w, e)
				return
			}
			writeJSON(w, 200, JSON{"session": session})
			return
		case "poll":
			var b struct {
				Generation int `json:"generation"`
			}
			if decodeBody(r, &b) != nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			commands, blocked, e := s.Web.PollBrowser(owner, parts[1], key, b.Generation)
			if e != nil {
				webError(w, e)
				return
			}
			writeJSON(w, 200, JSON{"commands": commands, "blocked_unknown": blocked})
			return
		case "close":
			var b struct {
				Generation int `json:"generation"`
			}
			if decodeBody(r, &b) != nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			if e := s.Web.CloseBrowserSession(owner, parts[1], key, b.Generation); e != nil {
				webError(w, e)
				return
			}
			writeJSON(w, 200, JSON{"status": "closed"})
			return
		}
	}
	if len(parts) == 3 && parts[0] == "commands" && r.Method == "POST" {
		switch parts[2] {
		case "begin":
			var b struct {
				Generation int `json:"generation"`
			}
			if decodeBody(r, &b) != nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			accepted, c, e := s.Web.BeginBrowserCommand(r.Context(), owner, parts[1], key, b.Generation)
			if e != nil {
				webError(w, e)
				return
			}
			writeJSON(w, 200, JSON{"accepted": accepted, "command": c})
			return
		case "result":
			var b struct {
				Generation int    `json:"generation"`
				Status     string `json:"status"`
				Result     JSON   `json:"result"`
				ErrorCode  string `json:"error_code"`
			}
			if decodeBody(r, &b) != nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			c, e := s.Web.CompleteBrowserCommand(owner, parts[1], key, b.Generation, b.Status, b.Result, b.ErrorCode)
			if e != nil {
				webError(w, e)
				return
			}
			writeJSON(w, 200, c)
			return
		case "reconcile":
			var b struct {
				Revision int `json:"revision"`
			}
			if decodeBody(r, &b) != nil {
				apiError(w, 422, "invalid_request", false)
				return
			}
			run, e := s.Web.ReconcileBrowserCommand(r.Context(), owner, parts[1], b.Revision)
			if e != nil {
				webError(w, e)
				return
			}
			writeJSON(w, 200, runView(run, s.Host))
			return
		}
	}
	if len(parts) == 2 && parts[0] == "commands" && r.Method == "GET" {
		c, e := s.Web.command(owner, parts[1])
		if e != nil {
			webError(w, e)
			return
		}
		if !s.Web.sessionKeyMatches(owner, c.SessionID, key) {
			apiError(w, 401, "browser_session_invalid", false)
			return
		}
		writeJSON(w, 200, c)
		return
	}
	http.NotFound(w, r)
}
