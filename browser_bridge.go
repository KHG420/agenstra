package agenstra

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
)

func (w *WebIntegration) browserRegistration(ctx context.Context, owner, integration, handlerVersion string, handlers []string) (*compiledFrontend, error) {
	p := w.profiles[integration]
	if !w.Config.BrowserBridge || p == nil {
		return nil, hostError("browser_integration_unavailable")
	}
	if _, e := w.Host.policy(ctx, owner, integration, true); e != nil {
		return nil, e
	}
	if handlerVersion != p.profile.HandlerVersion || len(handlers) > 100 {
		return nil, hostError("browser_handler_version_mismatch")
	}
	names := map[string]bool{}
	for _, a := range p.profile.Actions {
		names[a.Name] = true
	}
	seen := map[string]bool{}
	for _, n := range handlers {
		if !names[n] || seen[n] {
			return nil, hostError("browser_handler_unknown")
		}
		seen[n] = true
	}
	if _, e := w.release(ctx, owner, integration); e != nil {
		return nil, e
	}
	return p, nil
}
func (w *WebIntegration) CreateBrowserSession(ctx context.Context, owner, integration, handlerVersion string, handlers []string) (BrowserSession, string, error) {
	p, e := w.browserRegistration(ctx, owner, integration, handlerVersion, handlers)
	if e != nil {
		return BrowserSession{}, "", e
	}
	key := randomWebKey()
	s := BrowserSession{ID: NewID(), IntegrationID: integration, ProfileDigest: p.digest, Generation: 1, HandlerVersion: handlerVersion, Handlers: handlers, Context: JSON{}, ContextRevision: 0, LastSeen: w.Store.store.now(), KeyHash: webHash(key)}
	e = w.Store.store.write(func(tx *sql.Tx) error { return webInsert(tx, "web_sessions", s.ID, owner, s) })
	s.KeyHash = ""
	return s, key, e
}
func browserSessionAuth(s BrowserSession, key string, generation int) error {
	hash := webHash(key)
	if subtle.ConstantTimeCompare([]byte(hash), []byte(s.KeyHash)) != 1 || key == "" {
		return hostError("browser_session_invalid")
	}
	if generation != s.Generation || s.Closed {
		return hostError("browser_generation_changed")
	}
	return nil
}
func (w *WebIntegration) ResumeBrowserSession(owner, id, key string, generation int) (BrowserSession, error) {
	var s BrowserSession
	e := w.Store.store.write(func(tx *sql.Tx) error {
		if e := webLoad(tx, "web_sessions", id, owner, &s); e != nil {
			return e
		}
		if e := browserSessionAuth(s, key, generation); e != nil {
			return e
		}
		if p := w.profiles[s.IntegrationID]; p == nil || p.digest != s.ProfileDigest {
			return hostError("browser_profile_changed")
		}
		commands, e := w.Store.commands(tx, owner, id)
		if e != nil {
			return e
		}
		for _, c := range commands {
			switch c.Status {
			case "running":
				c.Status = "unknown"
				c.ErrorCode = "browser_connection_replaced"
			case "queued", "dispatched":
				c.Status = "cancelled"
				c.ErrorCode = "browser_connection_replaced"
			default:
				continue
			}
			if e = webSave(tx, "web_commands", c.ID, c); e != nil {
				return e
			}
		}
		s.Generation++
		s.LastSeen = w.Store.store.now()
		s.Context = JSON{}
		s.ContextRevision++
		return webSave(tx, "web_sessions", id, s)
	})
	s.KeyHash = ""
	return s, e
}

// RecoverBrowserSession replaces a stopped tab connection without changing or
// replaying its run/command history. Unknown outcomes require host verification.
// A stable request ID also recovers the same replacement after a lost response.
func (w *WebIntegration) RecoverBrowserSession(ctx context.Context, owner, id, key string, generation int, handlerVersion string, handlers []string, requestID string, acknowledgeUnknown bool) (BrowserSession, string, error) {
	var old, replacement BrowserSession
	if requestID == "" || len(requestID) > 128 {
		return replacement, "", hostError("request_id_required")
	}
	if e := webLoad(w.Store.store.DB, "web_sessions", id, owner, &old); e != nil {
		return replacement, "", e
	}
	if subtle.ConstantTimeCompare([]byte(webHash(key)), []byte(old.KeyHash)) != 1 || key == "" {
		return replacement, "", hostError("browser_session_invalid")
	}
	p, e := w.browserRegistration(ctx, owner, old.IntegrationID, handlerVersion, handlers)
	if e != nil {
		return replacement, "", e
	}
	newID := RequestRunID(owner, "browser-recovery:"+id+":"+requestID)
	newKey := webHash(JSON{"session_id": id, "browser_key": key, "request_id": requestID, "handler_version": handlerVersion, "handlers": handlers, "acknowledge_unknown": acknowledgeUnknown})
	e = w.Store.store.write(func(tx *sql.Tx) error {
		if e := webLoad(tx, "web_sessions", id, owner, &old); e != nil {
			return e
		}
		if old.Generation != generation {
			return hostError("browser_generation_changed")
		}
		// The same request can only retrieve its original, still-open replacement.
		if e := webLoad(tx, "web_sessions", newID, owner, &replacement); e == nil {
			if !old.Closed || replacement.Closed || replacement.KeyHash != webHash(newKey) || replacement.ProfileDigest != p.digest || replacement.HandlerVersion != handlerVersion || webHash(replacement.Handlers) != webHash(handlers) {
				return hostError("browser_recovery_conflict")
			}
			return nil
		} else if ErrorCode(e) != "not_found" {
			return e
		}
		if old.Closed {
			return hostError("browser_generation_changed")
		}
		commands, e := w.Store.commands(tx, owner, id)
		if e != nil {
			return e
		}
		for _, c := range commands {
			if c.Status == "running" {
				return hostError("browser_recovery_busy")
			}
			if c.Status == "unknown" && !acknowledgeUnknown {
				return hostError("browser_outcome_unresolved")
			}
		}
		// Check every bound run, including other conversations and unpublished chat.
		rows, e := tx.Query("SELECT run FROM web_bindings WHERE owner=? AND payload->>'session_id'=?", owner, id)
		if e != nil {
			return e
		}
		runs := []string{}
		for rows.Next() {
			var run string
			if e = rows.Scan(&run); e != nil {
				break
			}
			runs = append(runs, run)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
		for _, runID := range runs {
			run, e := w.Host.Store.GetRun(runID, owner)
			if e == nil {
				if !terminal(run.Status) {
					return hostError("browser_recovery_run_active")
				}
				continue
			}
			if !errors.Is(e, ErrRunNotFound) {
				return e
			}
			var status string
			e = tx.QueryRow("SELECT payload->>'status' FROM web_messages WHERE owner=? AND payload->>'run_id'=?", owner, runID).Scan(&status)
			if e == nil && !terminal(status) {
				return hostError("browser_recovery_run_active")
			}
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		for _, c := range commands {
			if c.Status == "queued" || c.Status == "dispatched" {
				c.Status = "cancelled"
				c.ErrorCode = "browser_connection_recovered"
				if e := webSave(tx, "web_commands", c.ID, c); e != nil {
					return e
				}
			}
		}
		old.Closed = true
		if e := webSave(tx, "web_sessions", id, old); e != nil {
			return e
		}
		replacement = BrowserSession{ID: newID, IntegrationID: old.IntegrationID, ProfileDigest: p.digest, Generation: 1, HandlerVersion: handlerVersion, Handlers: handlers, Context: JSON{}, LastSeen: w.Store.store.now(), KeyHash: webHash(newKey)}
		return webInsert(tx, "web_sessions", newID, owner, replacement)
	})
	replacement.KeyHash = ""
	if e != nil {
		return BrowserSession{}, "", e
	}
	return replacement, newKey, nil
}
func (w *WebIntegration) frontend(digest string) (*compiledFrontend, error) {
	for _, p := range w.profiles {
		if p.digest == digest {
			return p, nil
		}
	}
	rows, e := w.Store.store.DB.Query("SELECT payload FROM web_releases")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var r webRelease
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = webDecode(raw, &r); e != nil {
			return nil, e
		}
		p, e := compileFrontend(r.Profile)
		if e != nil {
			return nil, e
		}
		if p.digest == digest {
			return p, nil
		}
	}
	return nil, hostError("browser_profile_unavailable")
}

// UpdatePageObservation records current host page data for browser capabilities.
// It cannot select, replace or otherwise mutate an agent's conversation context.
func (w *WebIntegration) UpdatePageObservation(owner, id, key string, generation, revision int, data JSON) (BrowserSession, error) {
	var current BrowserSession
	if e := webLoad(w.Store.store.DB, "web_sessions", id, owner, &current); e != nil {
		return current, e
	}
	p, e := w.frontend(current.ProfileDigest)
	if e != nil {
		return current, e
	}
	raw, e := CanonicalJSON(data)
	if e != nil || len(raw) > 16384 || validateSchema(p.context, data) != nil {
		return current, hostError("browser_context_invalid")
	}
	e = w.Store.store.write(func(tx *sql.Tx) error {
		if e := webLoad(tx, "web_sessions", id, owner, &current); e != nil {
			return e
		}
		if e := browserSessionAuth(current, key, generation); e != nil {
			return e
		}
		if revision != current.ContextRevision {
			return hostError("revision_conflict")
		}
		current.Context = data
		current.ContextRevision++
		current.LastSeen = w.Store.store.now()
		return webSave(tx, "web_sessions", id, current)
	})
	current.KeyHash = ""
	return current, e
}
func (w *WebIntegration) readContext(owner string, b WebRunBinding) (BrowserSession, error) {
	var s BrowserSession
	e := w.Store.store.write(func(tx *sql.Tx) error {
		var raw string
		if e := tx.QueryRow("SELECT payload FROM web_bindings WHERE run=? AND owner=?", b.RunID, owner).Scan(&raw); e != nil {
			return e
		}
		if e := webDecode(raw, &b); e != nil {
			return e
		}
		if e := webLoad(tx, "web_sessions", b.SessionID, owner, &s); e != nil {
			return e
		}
		if s.Closed || s.Generation != b.Generation || w.Store.store.now()-s.LastSeen > 30 {
			return hostError("browser_offline")
		}
		b.ObservedRevision = s.ContextRevision
		raw, e := webJSON(b)
		if e != nil {
			return e
		}
		_, e = tx.Exec("UPDATE web_bindings SET payload=? WHERE run=? AND owner=?", raw, b.RunID, owner)
		return e
	})
	return s, e
}
func (w *WebIntegration) enqueue(owner string, b WebRunBinding, id, action string, args JSON, p *compiledFrontend) (BrowserCommand, error) {
	var c BrowserCommand
	e := w.Store.store.write(func(tx *sql.Tx) error {
		existing := webLoad(tx, "web_commands", id, owner, &c)
		if existing == nil {
			if c.RunID != b.RunID || c.Action != action || c.ArgumentsSHA256 != webHash(args) {
				return hostError("browser_command_conflict")
			}
			return nil
		}
		if ErrorCode(existing) != "not_found" {
			return existing
		}
		var raw string
		if e := tx.QueryRow("SELECT payload FROM web_bindings WHERE run=? AND owner=?", b.RunID, owner).Scan(&raw); e != nil {
			return e
		}
		if e := webDecode(raw, &b); e != nil {
			return e
		}
		var s BrowserSession
		if e := webLoad(tx, "web_sessions", b.SessionID, owner, &s); e != nil {
			return e
		}
		if s.Closed || s.Generation != b.Generation || w.Store.store.now()-s.LastSeen > 30 {
			return hostError("browser_offline")
		}
		if s.ProfileDigest != p.digest || !containsString(s.Handlers, action) {
			return hostError("browser_handler_unavailable")
		}
		if b.ObservedRevision < 0 {
			return hostError("browser_context_required")
		}
		commands, e := w.Store.commands(tx, owner, s.ID)
		if e != nil {
			return e
		}
		for _, old := range commands {
			if old.Status == "unknown" {
				return hostError("browser_outcome_unresolved")
			}
		}
		var timeout int
		for _, a := range p.profile.Actions {
			if a.Name == action {
				timeout = a.TimeoutSeconds
			}
		}
		if timeout == 0 {
			return hostError("capability_unknown")
		}
		c = BrowserCommand{ID: id, RunID: b.RunID, SessionID: s.ID, Generation: s.Generation, ProfileDigest: p.digest, Action: action, Arguments: args, ArgumentsSHA256: webHash(args), ContextRevision: b.ObservedRevision, Status: "queued", ExpiresAt: w.Store.store.now() + float64(timeout)}
		raw, e = webJSON(c)
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO web_commands(id,owner,session,run,payload) VALUES(?,?,?,?,?)", c.ID, owner, s.ID, b.RunID, raw)
		return e
	})
	return c, e
}
func expireCommand(c *BrowserCommand, now float64) {
	if now < c.ExpiresAt {
		return
	}
	switch c.Status {
	case "queued", "dispatched":
		c.Status = "expired"
		c.ErrorCode = "browser_command_expired"
	case "running":
		c.Status = "unknown"
		c.ErrorCode = "browser_outcome_unknown"
	}
}
func (w *WebIntegration) command(owner, id string) (BrowserCommand, error) {
	var c BrowserCommand
	e := w.Store.store.write(func(tx *sql.Tx) error {
		if e := webLoad(tx, "web_commands", id, owner, &c); e != nil {
			return e
		}
		expireCommand(&c, w.Store.store.now())
		return webSave(tx, "web_commands", id, c)
	})
	return c, e
}
func (w *WebIntegration) PollBrowser(owner, id, key string, generation int) ([]BrowserCommand, bool, error) {
	commands := []BrowserCommand{}
	blocked := false
	e := w.Store.store.write(func(tx *sql.Tx) error {
		var s BrowserSession
		if e := webLoad(tx, "web_sessions", id, owner, &s); e != nil {
			return e
		}
		if e := browserSessionAuth(s, key, generation); e != nil {
			return e
		}
		s.LastSeen = w.Store.store.now()
		if e := webSave(tx, "web_sessions", id, s); e != nil {
			return e
		}
		all, e := w.Store.commands(tx, owner, id)
		if e != nil {
			return e
		}
		for i := range all {
			expireCommand(&all[i], w.Store.store.now())
			if e = webSave(tx, "web_commands", all[i].ID, all[i]); e != nil {
				return e
			}
			if all[i].Status == "unknown" {
				blocked = true
			}
		}
		if blocked {
			return nil
		}
		for _, c := range all {
			if c.Generation != generation {
				continue
			}
			if c.Status == "running" {
				return nil
			}
			if c.Status == "queued" || c.Status == "dispatched" {
				c.Status = "dispatched"
				if e = webSave(tx, "web_commands", c.ID, c); e != nil {
					return e
				}
				commands = append(commands, c)
				break
			}
		}
		return nil
	})
	return commands, blocked, e
}
func (w *WebIntegration) BeginBrowserCommand(ctx context.Context, owner, id, key string, generation int) (bool, BrowserCommand, error) {
	current, e := w.command(owner, id)
	if e != nil {
		return false, current, e
	}
	if e = w.authorizeCommand(ctx, owner, current); e != nil {
		return false, current, e
	}
	var c BrowserCommand
	accepted := false
	e = w.Store.store.write(func(tx *sql.Tx) error {
		if e := webLoad(tx, "web_commands", id, owner, &c); e != nil {
			return e
		}
		var s BrowserSession
		if e := webLoad(tx, "web_sessions", c.SessionID, owner, &s); e != nil {
			return e
		}
		if e := browserSessionAuth(s, key, generation); e != nil {
			return e
		}
		if c.Generation != generation {
			return hostError("browser_generation_changed")
		}
		expireCommand(&c, w.Store.store.now())
		if c.Status == "dispatched" {
			if c.ContextRevision != s.ContextRevision {
				c.Status = "failed"
				c.ErrorCode = "browser_context_changed"
			} else {
				c.Status = "running"
				accepted = true
			}
		}
		return webSave(tx, "web_commands", id, c)
	})
	return accepted, c, e
}
func (w *WebIntegration) CompleteBrowserCommand(owner, id, key string, generation int, status string, result JSON, errorCode string) (BrowserCommand, error) {
	c, e := w.command(owner, id)
	if e != nil {
		return c, e
	}
	if status != "succeeded" && status != "failed" && status != "unknown" {
		return c, hostError("browser_result_invalid")
	}
	if status != "succeeded" && !safeCodePattern.MatchString(errorCode) {
		return c, hostError("browser_result_invalid")
	}
	if status == "succeeded" {
		p, e := w.frontend(c.ProfileDigest)
		if e != nil {
			return c, e
		}
		raw, e := CanonicalJSON(result)
		if e != nil || len(raw) > 65536 || validateSchema(p.outputs[c.Action], result) != nil {
			return c, hostError("browser_result_invalid")
		}
	}
	e = w.Store.store.write(func(tx *sql.Tx) error {
		if e := webLoad(tx, "web_commands", id, owner, &c); e != nil {
			return e
		}
		var s BrowserSession
		if e := webLoad(tx, "web_sessions", c.SessionID, owner, &s); e != nil {
			return e
		}
		// A cached result from the original generation can settle its original command.
		// It cannot claim or execute any command in the new generation.
		hash := webHash(key)
		if subtle.ConstantTimeCompare([]byte(hash), []byte(s.KeyHash)) != 1 || key == "" || c.Generation != generation {
			return hostError("browser_session_invalid")
		}
		if c.Status == status && webHash(c.Result) == webHash(result) && c.ErrorCode == errorCode {
			return nil
		}
		if c.Status != "running" && c.Status != "unknown" {
			return hostError("browser_result_conflict")
		}
		c.Status = status
		c.Result = result
		c.ErrorCode = errorCode
		return webSave(tx, "web_commands", id, c)
	})
	return c, e
}
func (w *WebIntegration) CloseBrowserSession(owner, id, key string, generation int) error {
	return w.Store.store.write(func(tx *sql.Tx) error {
		var s BrowserSession
		if e := webLoad(tx, "web_sessions", id, owner, &s); e != nil {
			return e
		}
		if e := browserSessionAuth(s, key, generation); e != nil {
			return e
		}
		s.Closed = true
		return webSave(tx, "web_sessions", id, s)
	})
}
func (w *WebIntegration) cancelCommands(owner, run string) error {
	return w.Store.store.write(func(tx *sql.Tx) error {
		rows, e := tx.Query("SELECT payload FROM web_commands WHERE owner=? AND run=?", owner, run)
		if e != nil {
			return e
		}
		items := []BrowserCommand{}
		for rows.Next() {
			var raw string
			var c BrowserCommand
			if e = rows.Scan(&raw); e != nil {
				break
			}
			if e = webDecode(raw, &c); e != nil {
				break
			}
			items = append(items, c)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
		for _, c := range items {
			switch c.Status {
			case "running":
				c.Status = "unknown"
				c.ErrorCode = "browser_run_cancelled"
			case "queued", "dispatched":
				c.Status = "cancelled"
				c.ErrorCode = "browser_run_cancelled"
			default:
				continue
			}
			if e = webSave(tx, "web_commands", c.ID, c); e != nil {
				return e
			}
		}
		return nil
	})
}

// ReconcileBrowserCommand re-polls only the existing operation after a verified
// original browser result arrives. It never creates or executes a new command.
func (w *WebIntegration) ReconcileBrowserCommand(ctx context.Context, owner, id string, revision int) (StoredRun, error) {
	c, e := w.command(owner, id)
	if e != nil {
		return StoredRun{}, e
	}
	if c.Status != "succeeded" && c.Status != "failed" {
		return StoredRun{}, hostError("browser_result_not_verified")
	}
	current, e := w.Host.Get(ctx, c.RunID, owner)
	if e != nil {
		return current, e
	}
	if current.Status != "needs_reconciliation" {
		return current, hostError("reconciliation_not_required")
	}
	run, e := w.Host.Store.Claim(c.RunID, owner, w.Host.Settings.LeaseSeconds)
	if e != nil {
		return run, e
	}
	defer w.Host.Store.Release(c.RunID, owner, run.LeaseToken)
	if run.Revision != revision {
		return run, hostError("revision_conflict")
	}
	state, e := w.Host.restore(run)
	if e != nil {
		return run, e
	}
	found := false
	for i := range state.Pending {
		item := &state.Pending[i]
		if item.InvocationID == c.ID && item.Status == "unknown" && item.Operation != nil && item.Operation.OperationID == c.ID && containsString(item.Operation.Binding.ReconciliationStates, "unknown") {
			item.Status = "waiting"
			item.ErrorCode = nil
			item.Operation.NextPollAt = w.Host.now()
			item.Operation.Deadline = w.Host.now() + 10
			found = true
		}
	}
	if !found {
		return run, hostError("reconciliation_binding_mismatch")
	}
	state.Status = "queued"
	state.ErrorCode = nil
	return w.Host.save(run, state, "", nil, map[string]any{"kind": "browser_result_reconciled", "invocation_id": c.ID})
}
func (w *WebIntegration) bindRun(tx *sql.Tx, owner, run, request, integration, session string) error {
	p := w.profiles[integration]
	if p == nil {
		return nil
	}
	var s BrowserSession
	if e := webLoad(tx, "web_sessions", session, owner, &s); e != nil {
		return e
	}
	if s.Closed || s.IntegrationID != integration || s.ProfileDigest != p.digest || w.Store.store.now()-s.LastSeen > 30 {
		return hostError("browser_session_unavailable")
	}
	b := WebRunBinding{RunID: run, IntegrationID: integration, SessionID: session, Generation: s.Generation, ProfileDigest: s.ProfileDigest, RequestID: request, ObservedRevision: -1}
	raw, e := webJSON(b)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO web_bindings(run,owner,payload) VALUES(?,?,?)", run, owner, raw)
	return e
}

// authorizeCommand rechecks live grants, cancellation and the exact approved
// invocation immediately before the browser may begin the handler.
func (w *WebIntegration) authorizeCommand(ctx context.Context, owner string, c BrowserCommand) error {
	b, e := w.Store.binding(owner, c.RunID)
	if e != nil {
		return e
	}
	if b.SessionID != c.SessionID || b.Generation != c.Generation || b.ProfileDigest != c.ProfileDigest {
		return hostError("browser_binding_mismatch")
	}
	policy, e := w.Host.policy(ctx, owner, b.IntegrationID, true)
	if e != nil {
		return e
	}
	if !policy.GrantedCapabilities[c.Action] {
		return hostError("capability_not_granted")
	}
	run, e := w.Host.Get(ctx, c.RunID, owner)
	if e != nil {
		return e
	}
	if run.CancelRequested || terminal(run.Status) {
		return hostError("browser_run_cancelled")
	}
	state, e := w.Host.restore(run)
	if e != nil {
		return e
	}
	profile, e := w.frontend(c.ProfileDigest)
	if e != nil {
		return e
	}
	var required bool
	for _, a := range profile.profile.Actions {
		if a.Name == c.Action {
			required = a.ApprovalRequired
		}
	}
	for _, item := range state.Pending {
		if item.InvocationID != c.ID {
			continue
		}
		if item.Call.Capability != c.Action || webHash(item.Call.Arguments) != c.ArgumentsSHA256 {
			return hostError("browser_arguments_changed")
		}
		if item.Status != "in_flight" && item.Status != "waiting" {
			return hostError("browser_command_not_pending")
		}
		if required || policy.ApprovalCapabilities[c.Action] {
			if item.ApprovedHash == nil || *item.ApprovedHash != item.ArgumentsSHA256 || item.ApprovedUntil == nil || w.Host.now() >= *item.ApprovedUntil {
				return hostError("browser_approval_expired")
			}
		}
		return nil
	}
	return hostError("browser_command_not_pending")
}
