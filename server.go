package agenstra

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HTTPServer struct {
	Host                *AgentHost
	Deployment          *Deployment
	WorkerEnabled       bool
	WorkerInterval      time.Duration
	mu                  sync.RWMutex
	ready, workerFailed bool
	stop                chan struct{}
	done                chan struct{}
	workerCtx           context.Context
	cancel              context.CancelFunc
	handler             http.Handler
	Web                 *WebIntegration
}

func NewHTTPServer(host *AgentHost, deployment *Deployment, workerEnabled bool, interval time.Duration) (*HTTPServer, error) {
	if host == nil || host.Store == nil || deployment == nil {
		return nil, fmt.Errorf("host and deployment required")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("worker interval must be positive")
	}
	if err := configureDeploymentChecks(host, deployment); err != nil {
		return nil, err
	}
	s := &HTTPServer{Host: host, Deployment: deployment, WorkerEnabled: workerEnabled, WorkerInterval: interval, stop: make(chan struct{}), done: make(chan struct{})}
	registryOpened := false
	if deployment.Registry != nil {
		if _, e := deployment.AdminKey(); e != nil {
			return nil, e
		}
		registryOpened = deployment.Registry.db == nil
		if e := deployment.Registry.Initialize(); e != nil {
			return nil, e
		}
	}
	if e := host.Store.Initialize(); e != nil {
		if registryOpened {
			_ = deployment.Registry.Close()
		}
		return nil, e
	}
	if deployment.Config.WebIntegration != nil {
		var e error
		s.Web, e = NewWebIntegration(host, deployment, *deployment.Config.WebIntegration)
		if e != nil {
			if registryOpened {
				_ = deployment.Registry.Close()
			}
			return nil, e
		}
	}
	s.handler = http.HandlerFunc(s.serveHTTP)
	s.workerCtx, s.cancel = context.WithCancel(context.Background())
	s.ready = true
	if workerEnabled {
		go s.worker()
	} else {
		close(s.done)
	}
	return s, nil
}
func (s *HTTPServer) Handler() http.Handler { return s.handler }
func (s *HTTPServer) Close() error {
	s.mu.Lock()
	if !s.ready {
		s.mu.Unlock()
		return nil
	}
	s.ready = false
	s.mu.Unlock()
	s.cancel()
	close(s.stop)
	<-s.done
	if s.Web != nil {
		return s.Web.Close()
	}
	return nil
}
func (s *HTTPServer) worker() {
	defer close(s.done)
	ticker := time.NewTicker(s.WorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		var err error
		if s.Web != nil {
			err = s.Web.Tick(s.workerCtx)
		}
		if err == nil {
			_, scheduleErr := s.Host.DispatchDueSchedules(s.workerCtx, 100)
			_, runErr := s.Host.WakeDue(s.workerCtx, 100)
			err = errors.Join(scheduleErr, runErr)
		}
		if s.workerCtx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.workerFailed = err != nil
		s.mu.Unlock()
		if err != nil {
			log.Print("agent worker cycle failed")
		}
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
	}
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, status int, code string, detail bool) {
	if detail {
		writeJSON(w, status, map[string]any{"detail": map[string]any{"code": code}})
	} else {
		writeJSON(w, status, map[string]any{"code": code})
	}
}
func serverError(w http.ResponseWriter, e error) {
	var dep *DeploymentError
	if errors.As(e, &dep) {
		status := 503
		if dep.Code == "access_denied" || dep.Code == "identity_unverified" {
			status = 403
		}
		apiError(w, status, dep.Code, false)
		return
	}
	var host *HostError
	if errors.As(e, &host) {
		status := 409
		switch host.Code {
		case "not_found":
			status = 404
		case "access_denied", "forbidden", "identity_unverified", "model_data_not_authorized", "capability_not_granted":
			status = 403
		case "authorization_unavailable", "connection_unavailable", "reconciliation_unavailable":
			status = 503
		case "source_scope_invalid", "schedule_invalid", "invalid_limit", "invalid_page", "memory_invalid", "steering_invalid", "reconciliation_invalid", "context_policy_invalid", "input_invalid":
			status = 422
		}
		apiError(w, status, host.Code, false)
		return
	}
	if errors.Is(e, ErrRunNotFound) {
		apiError(w, 404, "not_found", false)
		return
	}
	if errors.Is(e, ErrStoreConflict) || errors.Is(e, ErrLeaseLost) {
		apiError(w, 409, "conflict", false)
		return
	}
	log.Print("request failed")
	apiError(w, 500, "internal_error", false)
}
func runView(run StoredRun, host *AgentHost) map[string]any {
	b, _ := json.Marshal(run)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	delete(m, "lease_token")
	delete(m, "lease_until")
	if telemetry, err := host.telemetry(run); err == nil {
		m["telemetry"] = telemetry
	}
	return m
}
func decodeBody(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 2<<20)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if e := dec.Decode(dst); e != nil {
		return e
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
func (s *HTTPServer) owner(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", deploymentError("unauthorized")
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" || token != strings.TrimSpace(token) {
		return "", deploymentError("unauthorized")
	}
	return s.Deployment.AuthenticateContext(r.Context(), token)
}
func (s *HTTPServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if s.Web != nil && (strings.HasPrefix(p, "/web/") || strings.HasPrefix(p, "/chat/v1/") || strings.HasPrefix(p, "/browser/v1/")) {
		s.webHTTP(w, r)
		return
	}
	if p == "/healthz" && r.Method == "GET" {
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if p == "/readyz" && r.Method == "GET" {
		s.mu.RLock()
		ready := s.ready && !s.workerFailed
		s.mu.RUnlock()
		if ready {
			_, e := s.Host.Store.ListRuns("__health__", 1)
			ready = e == nil
		}
		if ready {
			writeJSON(w, 200, map[string]string{"status": "ready"})
		} else {
			writeJSON(w, 503, map[string]string{"status": "unavailable"})
		}
		return
	}
	if strings.HasPrefix(p, "/admin") {
		s.adminHTTP(w, r)
		return
	}
	owner, e := s.owner(r)
	if e != nil {
		apiError(w, 401, "unauthorized", true)
		return
	}
	if p == "/memories" || strings.HasPrefix(p, "/memories/") {
		s.memoriesHTTP(w, r, owner, "")
		return
	}
	if p == "/schedules" || strings.HasPrefix(p, "/schedules/") {
		s.schedulesHTTP(w, r, owner)
		return
	}
	if p == "/runtime-info" {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		info, err := s.Host.GetRuntimeInfo(r.Context(), owner, r.URL.Query().Get("pack_id"))
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 200, info)
		return
	}
	if p == "/runs" {
		s.runsHTTP(w, r, owner)
		return
	}
	if strings.HasPrefix(p, "/runs/") {
		s.runHTTP(w, r, owner)
		return
	}
	http.NotFound(w, r)
}
func (s *HTTPServer) runsHTTP(w http.ResponseWriter, r *http.Request, owner string) {
	switch r.Method {
	case "POST":
		var body struct {
			Sources     []RunSource `json:"sources,omitempty"`
			PackID      string      `json:"pack_id"`
			Instruction string      `json:"instruction"`
			RequestID   *string     `json:"request_id"`
		}
		if e := decodeBody(r, &body); e != nil || len(body.PackID) < 1 || len(body.PackID) > 128 || len(body.Instruction) < 1 || len(body.Instruction) > 30000 || (body.RequestID != nil && (len(*body.RequestID) < 1 || len(*body.RequestID) > 128)) {
			apiError(w, 422, "invalid_request", true)
			return
		}
		requestID := ""
		if body.RequestID != nil {
			requestID = *body.RequestID
		}
		run, e := s.Host.CreateWithSources(r.Context(), owner, body.PackID, body.Instruction, requestID, body.Sources)
		if e != nil {
			serverError(w, e)
			return
		}
		writeJSON(w, 200, runView(run, s.Host))
	case "GET":
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var e error
			limit, e = strconv.Atoi(raw)
			if e != nil {
				apiError(w, 422, "invalid_limit", true)
				return
			}
		}
		if limit < 1 || limit > 1000 {
			apiError(w, 422, "invalid_limit", true)
			return
		}
		runs, e := s.Host.Store.ListRuns(owner, limit)
		if e != nil {
			serverError(w, e)
			return
		}
		out := []map[string]any{}
		for _, run := range runs {
			visible, e := s.Host.Get(r.Context(), run.RunID, owner)
			if e == nil {
				out = append(out, runView(visible, s.Host))
			} else {
				var h *HostError
				if errors.Is(e, ErrRunNotFound) || errors.As(e, &h) && (h.Code == "access_denied" || h.Code == "forbidden" || h.Code == "identity_unverified" || h.Code == "model_data_not_authorized") {
					continue
				}
				serverError(w, e)
				return
			}
		}
		writeJSON(w, 200, out)
	default:
		w.WriteHeader(405)
	}
}
func (s *HTTPServer) runHTTP(w http.ResponseWriter, r *http.Request, owner string) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/runs/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	ctx := r.Context()
	if len(parts) == 1 {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		run, e := s.Host.Get(ctx, id, owner)
		if e != nil {
			serverError(w, e)
			return
		}
		writeJSON(w, 200, runView(run, s.Host))
		return
	}
	switch parts[1] {
	case "diagnostics":
		if len(parts) != 2 || r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		result, err := s.Host.GetDiagnostics(ctx, id, owner)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 200, result)
	case "context-policy":
		if len(parts) != 2 || r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var body struct {
			RequestID string        `json:"request_id"`
			Revision  *int          `json:"revision"`
			Policy    ContextPolicy `json:"policy"`
		}
		if decodeBody(r, &body) != nil || body.Revision == nil {
			apiError(w, 422, "context_policy_invalid", false)
			return
		}
		run, err := s.Host.SetContextPolicy(ctx, id, owner, body.RequestID, *body.Revision, body.Policy)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 202, runView(run, s.Host))
	case "telemetry":
		if len(parts) != 2 || r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		telemetry, err := s.Host.GetTelemetry(ctx, id, owner)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 200, telemetry)
	case "reconcile":
		if len(parts) != 2 || r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var b struct {
			InvocationID    string `json:"invocation_id"`
			ArgumentsSHA256 string `json:"arguments_sha256"`
			Revision        *int   `json:"revision"`
		}
		if decodeBody(r, &b) != nil || b.Revision == nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		run, err := s.Host.Reconcile(ctx, id, owner, b.InvocationID, b.ArgumentsSHA256, *b.Revision)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 200, runView(run, s.Host))
	case "steer":
		if len(parts) != 2 || r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var b struct {
			RequestID string `json:"request_id"`
			Text      string `json:"text"`
			Revision  *int   `json:"revision"`
		}
		if decodeBody(r, &b) != nil || b.Revision == nil {
			apiError(w, 422, "invalid_request", true)
			return
		}
		run, err := s.Host.Steer(ctx, id, owner, b.RequestID, b.Text, *b.Revision)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, 202, runView(run, s.Host))
	case "input":
		if len(parts) != 2 || r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var b struct {
			Field    string  `json:"field"`
			Text     *string `json:"text"`
			Revision *int    `json:"revision"`
		}
		if decodeBody(r, &b) != nil || b.Field == "" || b.Text == nil || b.Revision == nil || *b.Revision < 0 {
			apiError(w, 422, "invalid_request", true)
			return
		}
		run, e := s.Host.SupplyInput(ctx, id, owner, b.Field, *b.Text, *b.Revision)
		if e != nil {
			serverError(w, e)
			return
		}
		writeJSON(w, 200, runView(run, s.Host))
	case "approval":
		if len(parts) != 2 || r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var b struct {
			InvocationID    string `json:"invocation_id"`
			ArgumentsSHA256 string `json:"arguments_sha256"`
			Revision        *int   `json:"revision"`
			Approved        *bool  `json:"approved"`
		}
		if decodeBody(r, &b) != nil || b.InvocationID == "" || len(b.ArgumentsSHA256) != 64 || b.Revision == nil || *b.Revision < 0 {
			apiError(w, 422, "invalid_request", true)
			return
		}
		approved := true
		if b.Approved != nil {
			approved = *b.Approved
		}
		run, e := s.Host.Approve(ctx, id, owner, b.InvocationID, b.ArgumentsSHA256, *b.Revision, approved)
		if e != nil {
			serverError(w, e)
			return
		}
		writeJSON(w, 200, runView(run, s.Host))
	case "resume", "cancel":
		if len(parts) != 2 || r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var run StoredRun
		var e error
		if parts[1] == "resume" {
			run, e = s.Host.Drive(ctx, id, owner)
		} else {
			run, e = s.Host.Cancel(ctx, id, owner)
		}
		if e != nil {
			serverError(w, e)
			return
		}
		writeJSON(w, 200, runView(run, s.Host))
	case "events":
		if len(parts) != 2 || r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		after, ea := strconv.Atoi(defaultString(r.URL.Query().Get("after"), "0"))
		limit, el := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "100"))
		if ea != nil || el != nil || after < 0 || limit < 1 || limit > 1000 {
			apiError(w, 422, "invalid_page", true)
			return
		}
		if _, e := s.Host.Get(ctx, id, owner); e != nil {
			serverError(w, e)
			return
		}
		events, e := s.Host.Store.ListEvents(id, owner, after, limit)
		if e != nil {
			serverError(w, e)
			return
		}
		writeJSON(w, 200, events)
	case "artifacts":
		if len(parts) != 3 || r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		if _, e := s.Host.Get(ctx, id, owner); e != nil {
			serverError(w, e)
			return
		}
		a, e := s.Host.Store.GetArtifact(id, parts[2], owner)
		if e != nil {
			if errors.Is(e, ErrRunNotFound) {
				apiError(w, 404, "not_found", true)
			} else {
				serverError(w, e)
			}
			return
		}
		writeJSON(w, 200, a)
	default:
		http.NotFound(w, r)
	}
}
func defaultString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
func sameToken(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
