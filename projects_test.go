package agenstra

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type projectTestProvider struct {
	hostProvider
	subject string
	skills  map[string]Skill
}

func (p *projectTestProvider) BoundSubject() string     { return p.subject }
func (p *projectTestProvider) Skills() map[string]Skill { return p.skills }

func projectTestHost(t *testing.T, model DecisionModel) (*AgentHost, map[string]*projectTestProvider, map[string]ExecutionPolicy) {
	t.Helper()
	providers := map[string]*projectTestProvider{}
	policies := map[string]ExecutionPolicy{}
	for _, pack := range []string{"home", "location", "weather"} {
		providers[pack] = &projectTestProvider{hostProvider: hostProvider{caps: map[string]CapabilityDescription{
			"query":      {Name: "query", Version: "1", Effect: "read", Replay: "safe", InputSchema: JSON{"type": "object"}, ReferenceScope: "durable"},
			"admin.read": {Name: "admin.read", Version: "1", Effect: "read", Replay: "safe", InputSchema: JSON{"type": "object"}},
		}, binding: pack}, subject: "alice-" + pack}
		policies[pack] = ExecutionPolicy{AllowModelData: true, Subject: "alice-" + pack, PermissionsVerified: true, GrantedCapabilities: map[string]bool{"query": true, "admin.read": true}}
	}
	root := policies["home"]
	root.GrantedCapabilities = map[string]bool{"query": true}
	root.Delegations = map[string][]string{"location": {"query"}, "weather": {"query"}}
	policies["home"] = root
	h := NewAgentHost(testStore(t), func(_ context.Context, owner, pack string) (CapabilityProvider, error) {
		if owner != "alice" || providers[pack] == nil {
			return nil, hostError("access_denied")
		}
		return providers[pack], nil
	}, model, func(_ context.Context, owner, pack string) (ExecutionPolicy, error) {
		if owner != "alice" {
			return ExecutionPolicy{}, hostError("access_denied")
		}
		p, ok := policies[pack]
		if !ok {
			return p, hostError("access_denied")
		}
		return p, nil
	})
	return h, providers, policies
}
func source(pack string, names ...string) RunSource {
	return RunSource{PackID: pack, Capabilities: names}
}

func TestProjectTaskScopeDeniesAdminReadsAndExplicitReads(t *testing.T) {
	model := &hostModel{decisions: []Decision{callDecision("weather::admin.read")}}
	h, providers, _ := projectTestHost(t, model)
	model.hook = func(p ContextPacket) {
		for _, c := range p.Capabilities {
			if c["name"] == "weather::admin.read" || c["name"] == "admin.read" {
				t.Fatal("ungranted admin read exposed", c)
			}
		}
	}
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "Weather", "scope", []RunSource{source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatal(run.Status, err)
	}
	if providers["weather"].calls != 0 {
		t.Fatal("out-of-scope admin tool executed")
	}
	if _, err = h.CreateWithSources(t.Context(), "alice", "home", "Weather", "admin", []RunSource{source("weather", "admin.read")}); ErrorCode(err) != "capability_not_granted" {
		t.Fatal("admin authority escaped delegation ceiling", err)
	}
	p := providers["home"]
	outcome, err := ExecuteCall(t.Context(), p, nil, ToolCall{Capability: "query", Arguments: JSON{}}, nil)
	if err != nil || outcome.ErrorCode != "capability_not_granted" || p.calls != 0 {
		t.Fatal("ungranted read executed", outcome, err)
	}
	runtime := &AgentRuntime{Provider: p, Grants: map[string]bool{}}
	state, callErr := runtime.NewState("inspect", "inspect")
	if callErr != nil {
		t.Error(callErr)
	}
	runtime.Model = &hostModel{decisions: []Decision{{Schema: "agenstra.decision.v1", Kind: "inspect_capability", Name: "admin.read"}}}
	if err := runtime.Step(t.Context(), state, nil); err != nil || state.InspectedCapability != nil {
		t.Fatal("ungranted read inspection exposed", err)
	}
}

func TestProjectRevocationBeforeInvocationAndIdentityBinding(t *testing.T) {
	for _, revoke := range []string{"delegation", "target_permission", "subject", "credential_subject", "missing_verification"} {
		t.Run(revoke, func(t *testing.T) {
			model := &hostModel{decisions: []Decision{callDecision("weather::query")}}
			h, providers, policies := projectTestHost(t, model)
			run, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "", []RunSource{source("weather", "query")})
			if err != nil {
				t.Fatal(err)
			}
			mutate := func() {
				p := policies["weather"]
				switch revoke {
				case "delegation":
					root := policies["home"]
					root.Delegations = map[string][]string{}
					policies["home"] = root
				case "target_permission":
					p.GrantedCapabilities = map[string]bool{}
					policies["weather"] = p
				case "subject":
					p.Subject = "administrator"
					policies["weather"] = p
				case "credential_subject":
					providers["weather"].subject = "administrator"
				case "missing_verification":
					p.PermissionsVerified = false
					policies["weather"] = p
				}
			}
			if revoke == "credential_subject" || revoke == "missing_verification" {
				mutate()
			} else {
				model.hook = func(ContextPacket) { mutate() }
			}
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if providers["weather"].calls != 0 || run.Status != "needs_authorization" {
				t.Fatal("revoked call executed", revoke, run.Status, err)
			}
		})
	}
}

func TestProjectReleaseScopeAndPrivateMemorySnapshot(t *testing.T) {
	h, providers, _ := projectTestHost(t, &hostModel{})
	release := "v1"
	opened := map[string]string{}
	h.ReleaseResolver = func(context.Context, string, string) (string, error) { return release, nil }
	h.ReleaseProviderFactory = func(_ context.Context, owner, pack, version string) (CapabilityProvider, error) {
		opened[pack] = version
		return providers[pack], nil
	}
	for _, item := range []struct{ owner, pack, key, value string }{
		{"alice", "home", "report.language", "en"}, {"alice", "weather", "report.language", "zh-CN"}, {"alice", "weather", "query.units", "metric"},
	} {
		if _, err := h.SetMemory(t.Context(), item.owner, item.pack, MemoryUpdate{Scope: "pack", Key: item.key, Value: item.value, Kind: "preference"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Store.setMemory("bob", "home", MemoryUpdate{Scope: "pack", Key: "report.language", Value: "fr", Kind: "preference"}); err != nil {
		t.Fatal(err)
	}
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "stable", []RunSource{source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	release = "v2"
	repeated, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "stable", []RunSource{source("weather", "query")})
	if err != nil || repeated.RunID != run.RunID {
		t.Fatal(err)
	}
	if _, err = h.Create(t.Context(), "alice", "home", "weather", "stable"); ErrorCode(err) != "request_id_conflict" {
		t.Fatal("replay dropped source scope", err)
	}
	if _, err = h.CreateWithSources(t.Context(), "alice", "home", "weather", "stable", []RunSource{source("location", "query")}); ErrorCode(err) != "request_id_conflict" {
		t.Fatal("replay replaced source scope", err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if opened["home"] != "v1" || opened["weather"] != "v1" {
		t.Fatal("source versions not pinned", opened)
	}
	views, err := h.runMemories(run)
	if err != nil || len(views) != 3 {
		t.Fatal(views, err)
	}
	for _, v := range views {
		if v.Value == "fr" || v.PackID == "" {
			t.Fatal("private memory scope leaked", v)
		}
	}
	m, callErr2 := h.ListMemories(t.Context(), "alice", "weather", 100, 0)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	for _, v := range m {
		if v.Key == "query.units" {
			if _, err = h.DeleteMemory(t.Context(), "alice", "weather", v.ID, v.Revision); err != nil {
				t.Fatal(err)
			}
		}
	}
	views, err = h.runMemories(run)
	if err != nil || len(views) != 2 {
		t.Fatal("source forgetting did not invalidate snapshot", views, err)
	}
}

func TestProjectApprovalAndPollingCannotBypassRevokedGrant(t *testing.T) {
	model := &hostModel{decisions: []Decision{callDecision("weather::query")}}
	h, providers, policies := projectTestHost(t, model)
	cap := providers["weather"].caps["query"]
	cap.ApprovalRequired = true
	providers["weather"].caps["query"] = cap
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "", []RunSource{source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_approval" {
		t.Fatal(run.Status, err)
	}
	state, callErr3 := h.restore(run)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	item := state.Pending[0]
	policy := policies["weather"]
	policy.GrantedCapabilities = map[string]bool{}
	policies["weather"] = policy
	if _, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true); ErrorCode(err) != "capability_not_granted" {
		t.Fatal("approval enabled revoked grant", err)
	}
	if err = pollAccess(providers["weather"], policy, OperationBinding{PollCapability: "query"}); ErrorCode(err) != "capability_not_granted" {
		t.Fatal("poll bypassed read grant", err)
	}
}

func writeProjectPack(t *testing.T, dir, pack string) string {
	t.Helper()
	cap := JSON{"name": "query", "description": "Query " + pack, "method": "GET", "path": "/query", "effect": "read", "input_schema": JSON{"type": "object", "properties": JSON{"query": JSON{"type": "object", "properties": JSON{"city": JSON{"type": "string"}}, "additionalProperties": false}}, "additionalProperties": false}, "output_schema": JSON{"type": "object"}}
	admin := JSON{"name": "admin.read", "description": "Read admin secrets", "method": "GET", "path": "/admin", "effect": "read", "input_schema": JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}, "output_schema": JSON{"type": "object"}}
	raw, callErr4 := json.Marshal(JSON{"schema": "agenstra.rest-pack.v2", "name": pack, "version": "1.0.0", "guidance": "Use only this project's query conventions.", "base_url_env": "API_URL", "token_env": "API_TOKEN", "capabilities": []any{cap, admin}})
	if callErr4 != nil {
		t.Error(callErr4)
	}
	path := filepath.Join(dir, pack+".json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProjectVerifiedRESTLocationToWeatherEndToEnd(t *testing.T) {
	var locationCalls, weatherCalls atomic.Int32
	dir := t.TempDir()
	dep := &Deployment{Config: DeploymentConfig{Packs: map[string]PackConfig{}, Users: map[string]UserConfig{}}, Environment: map[string]string{}}
	connections := map[string]ConnectionConfig{}
	for _, pack := range []string{"home", "location", "weather"} {
		name := pack
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer alice-"+name+"-token" {
				w.WriteHeader(403)
				return
			}
			if r.URL.Path == "/me" {
				writeJSON(w, 200, JSON{"sub": "account-alice-" + name, "capabilities": []string{"query", "admin.read"}})
				return
			}
			if r.URL.Path == "/admin" {
				t.Error("administrator API executed")
				w.WriteHeader(403)
				return
			}
			if name == "location" {
				locationCalls.Add(1)
				writeJSON(w, 200, JSON{"city": "Shanghai"})
				return
			}
			if name == "weather" {
				weatherCalls.Add(1)
				if r.URL.Query().Get("city") != "Shanghai" {
					t.Error("location Fact reference was not forwarded")
				}
				writeJSON(w, 200, JSON{"forecast": "sunny"})
				return
			}
			t.Error("home backend called unexpectedly")
		}))
		t.Cleanup(srv.Close)
		dep.Config.Packs[name] = PackConfig{Path: writeProjectPack(t, dir, name)}
		prefix := strings.ToUpper(name)
		dep.Environment[prefix+"_URL"], dep.Environment[prefix+"_ME"], dep.Environment[prefix+"_TOKEN"] = srv.URL, srv.URL+"/me", "alice-"+name+"-token"
		connections[name] = ConnectionConfig{Environment: map[string]string{"API_URL": prefix + "_URL", "API_TOKEN": prefix + "_TOKEN"}, GrantedCapabilities: []string{"query", "admin.read"}, AllowModelData: true, Identity: &IdentityConfig{URLEnv: prefix + "_ME", TokenEnv: prefix + "_TOKEN", ExpectedSubject: "account-alice-" + name, CapabilitiesPath: []any{"capabilities"}}}
	}
	home := connections["home"]
	home.GrantedCapabilities = []string{}
	home.Delegations = map[string][]string{"location": {"query"}, "weather": {"query"}}
	connections["home"] = home
	dep.Config.Users["alice"] = UserConfig{Packs: connections}
	rounds := 0
	model := contextBudgetModel(func(_ context.Context, p ContextPacket, _ string) (Decision, error) {
		rounds++
		if p.OriginPackID != "home" {
			t.Fatal("origin lost", p.OriginPackID)
		}
		for _, c := range p.Capabilities {
			if strings.Contains(c["name"].(string), "admin") {
				t.Fatal("admin scope exposed to model")
			}
		}
		switch rounds {
		case 1:
			return Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "location", Capability: "location::query", Arguments: JSON{}, Reason: "Find location"}}}, nil
		case 2:
			return Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []ToolCall{{CallRef: "weather", Capability: "weather::query", Arguments: JSON{"query": JSON{"city": JSON{"$fact_value": JSON{"fact_id": p.Facts[0].FactID, "path": []any{"data", "city"}}}}}, Reason: "Query weather at user's location"}}}, nil
		default:
			ids := []string{}
			for _, f := range p.Facts {
				ids = append(ids, f.FactID)
			}
			return Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Shanghai: sunny", FactIDs: ids}, nil
		}
	})
	for _, name := range []string{"home", "location", "weather"} {
		p, err := dep.ProviderFactory(t.Context(), "alice", name)
		if err != nil {
			t.Fatal(name, err)
		}
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}
	h := NewAgentHost(testStore(t), dep.ProviderFactory, model, dep.PolicyResolver)
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "Today's weather", "", []RunSource{source("location", "query"), source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatal(run.Status, run.State, err)
	}
	state, callErr5 := h.restore(run)
	if callErr5 != nil {
		t.Error(callErr5)
	}
	if len(state.Facts) != 2 || locationCalls.Load() != 1 || weatherCalls.Load() != 1 {
		t.Fatal("composition did not execute once", state.Facts)
	}
	for _, f := range state.Facts {
		if f.SourceSubject != "account-alice-"+f.SourcePackID || !slices.Contains([]string{"location", "weather"}, f.SourcePackID) {
			t.Fatal("missing target provenance", f)
		}
	}
	events, err := h.Store.ListEvents(run.RunID, "alice", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, callErr6 := json.Marshal(events)
	if callErr6 != nil {
		t.Error(callErr6)
	}
	if !strings.Contains(string(raw), "account-alice-weather") || strings.Contains(string(raw), "-token") {
		t.Fatal("audit identity missing or credential leaked", string(raw))
	}
}

func TestProjectAuthorityFailureAndCredentialMismatch(t *testing.T) {
	for _, mode := range []string{"wrong_subject", "malformed_permissions", "empty_permissions", "outage", "missing_permissions", "mismatched_token"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "outage":
					w.WriteHeader(503)
				case "wrong_subject":
					writeJSON(w, 200, JSON{"sub": "admin", "capabilities": []string{"query"}})
				case "malformed_permissions":
					writeJSON(w, 200, JSON{"sub": "alice-b", "capabilities": []any{42}})
				case "empty_permissions":
					writeJSON(w, 200, JSON{"sub": "alice-b", "capabilities": []any{}})
				default:
					writeJSON(w, 200, JSON{"sub": "alice-b", "capabilities": []string{"query"}})
				}
			}))
			defer srv.Close()
			c := ConnectionConfig{Environment: map[string]string{"API_URL": "URL", "API_TOKEN": "TOKEN"}, GrantedCapabilities: []string{"query"}, AllowModelData: true, Identity: &IdentityConfig{URLEnv: "ME", TokenEnv: "TOKEN", ExpectedSubject: "alice-b", CapabilitiesPath: []any{"capabilities"}}}
			if mode == "missing_permissions" {
				c.Identity.CapabilitiesPath = nil
			}
			if mode == "mismatched_token" {
				c.Environment["API_TOKEN"] = "ADMIN_TOKEN"
			}
			dep := &Deployment{Config: DeploymentConfig{Packs: map[string]PackConfig{"b": {Path: writeProjectPack(t, t.TempDir(), "b")}}, Users: map[string]UserConfig{"alice": {Packs: map[string]ConnectionConfig{"b": c}}}}, Environment: map[string]string{"URL": srv.URL, "ME": srv.URL, "TOKEN": "alice-token", "ADMIN_TOKEN": "admin-token"}}
			h, _, policies := projectTestHost(t, &hostModel{})
			h.PolicyResolver = func(ctx context.Context, owner, pack string) (ExecutionPolicy, error) {
				if pack == "home" {
					p := policies["home"]
					p.Delegations = map[string][]string{"b": {"query"}}
					return p, nil
				}
				return dep.PolicyResolver(ctx, owner, pack)
			}
			h.ProviderFactory = func(ctx context.Context, owner, pack string) (CapabilityProvider, error) {
				if pack == "home" {
					return &hostProvider{}, nil
				}
				return dep.ProviderFactory(ctx, owner, pack)
			}
			run, err := h.CreateWithSources(t.Context(), "alice", "home", "Query B", "", []RunSource{source("b", "query")})
			if mode == "mismatched_token" {
				if err != nil {
					t.Fatal(err)
				}
				run, err = h.Drive(t.Context(), run.RunID, "alice")
				if run.Status != "needs_authorization" || ErrorCode(err) != "identity_unverified" {
					t.Fatal("mismatched credentials trusted", run.Status, err)
				}
			} else if err == nil {
				t.Fatal("unverified authority accepted", mode)
			}
		})
	}
}

func TestProjectSourceValidationAndOwnerIsolation(t *testing.T) {
	h, _, _ := projectTestHost(t, &hostModel{})
	for _, sources := range [][]RunSource{{source("home", "query")}, {source("weather", "query", "query")}, {source("weather", "other::query")}, {source("weather", "query"), source("weather", "query")}, {source("weather")}} {
		if _, err := h.CreateWithSources(t.Context(), "alice", "home", "query", "", sources); ErrorCode(err) != "source_scope_invalid" {
			t.Fatal(sources, err)
		}
	}
	if _, err := h.CreateWithSources(t.Context(), "bob", "home", "query", "", []RunSource{source("weather", "query")}); ErrorCode(err) != "access_denied" {
		t.Fatal("another owner's account reused", err)
	}
	if _, err := h.CreateWithSources(t.Context(), "alice", "home", "query", "", []RunSource{source("unknown", "query")}); ErrorCode(err) != "access_denied" {
		t.Fatal("missing account mapping trusted", err)
	}
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "query", "", []RunSource{source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Get(t.Context(), run.RunID, "bob"); !errors.Is(err, ErrRunNotFound) {
		t.Fatal("cross task ownership lost", err)
	}
}

func TestProjectScopeAcrossHTTPChatBrowserAndSchedules(t *testing.T) {
	t.Run("HTTP", func(t *testing.T) {
		h, _, _ := projectTestHost(t, &hostModel{})
		dep := &Deployment{Config: DeploymentConfig{Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY"}}}, Environment: map[string]string{"ALICE_KEY": "key"}}
		server, err := NewHTTPServer(h, dep, false, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func(close func() error) {
			if err := close(); err != nil {
				t.Error(err)
			}
		}(server.Close)
		request := func(body JSON) *httptest.ResponseRecorder {
			raw, callErr7 := json.Marshal(body)
			if callErr7 != nil {
				t.Error(callErr7)
			}
			req := httptest.NewRequest("POST", "/runs", strings.NewReader(string(raw)))
			req.Header.Set("Authorization", "Bearer key")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			return rec
		}
		body := JSON{"pack_id": "home", "instruction": "query weather", "request_id": "cross-http", "sources": []RunSource{source("weather", "query")}}
		rec := request(body)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "alice-weather") {
			t.Fatal(rec.Code, rec.Body.String())
		}
		delete(body, "sources")
		rec = request(body)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "request_id_conflict") {
			t.Fatal("HTTP replay changed scope", rec.Code, rec.Body.String())
		}
		body["request_id"] = "forge"
		body["target_subject"] = "administrator"
		rec = request(body)
		if rec.Code != 422 {
			t.Fatal("client provided identity accepted", rec.Code)
		}
	})
	for _, mode := range []string{"chat", "browser"} {
		t.Run(mode, func(t *testing.T) {
			f := newWebFixture(t, &hostModel{decisions: []Decision{callDecision("weather::query")}}, false)
			originalPolicy, originalFactory := f.w.basePolicy, f.w.baseFactory
			provider := &projectTestProvider{hostProvider: hostProvider{}, subject: "alice-weather"}
			f.w.basePolicy = func(ctx context.Context, owner, pack string) (ExecutionPolicy, error) {
				if pack == "weather" {
					return ExecutionPolicy{AllowModelData: true, Subject: "alice-weather", PermissionsVerified: true, GrantedCapabilities: map[string]bool{"records.get": true, "query": true}}, nil
				}
				p, err := originalPolicy(ctx, owner, pack)
				p.Delegations = map[string][]string{"weather": {"records.get"}}
				return p, err
			}
			f.w.baseFactory = func(ctx context.Context, owner, pack string) (CapabilityProvider, error) {
				if pack == "weather" {
					return provider, nil
				}
				return originalFactory(ctx, owner, pack)
			}
			sources := []RunSource{source("weather", "records.get")}
			if mode == "chat" {
				c, err := f.w.CreateConversation(t.Context(), "alice", "records")
				if err != nil {
					t.Fatal(err)
				}
				m, err := f.w.SubmitMessageWithSources(t.Context(), "alice", c.ID, "one", "Weather", "", sources)
				if err != nil || !equalSources(m.Sources, sources) {
					t.Fatal(m, err)
				}
				if _, err = f.w.SubmitMessage(t.Context(), "alice", c.ID, "one", "Weather", ""); ErrorCode(err) != "chat_message_conflict" {
					t.Fatal("chat replay dropped scope", err)
				}
				if err = f.w.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
				run, err := f.h.Get(t.Context(), m.RunID, "alice")
				if err != nil || !sourceScopeEqual(run, sources) {
					t.Fatal("chat did not persist scope", run, err)
				}
			} else {
				run, err := f.w.CreateBrowserRunWithSources(t.Context(), "alice", "records-web", f.session.ID, "Weather", "browser-cross", sources)
				if err != nil || !sourceScopeEqual(run, sources) {
					t.Fatal(run, err)
				}
				if _, err = f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Weather", "browser-cross"); ErrorCode(err) != "request_id_conflict" {
					t.Fatal("browser replay dropped scope", err)
				}
			}
		})
	}
	t.Run("schedule", func(t *testing.T) {
		h, _, policies := projectTestHost(t, &hostModel{})
		now := 1000.
		h.Clock = func() float64 { return now }
		h.Store.Clock = h.Clock
		task, err := h.CreateSchedule(t.Context(), "alice", ScheduleRequest{Name: "Weather", PackID: "home", Instruction: "Weather", Schedule: ScheduleSpec{Kind: "interval", IntervalSeconds: 60}, Sources: []RunSource{source("weather", "query")}})
		if err != nil {
			t.Fatal(err)
		}
		now = 1060.
		if n, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || n != 1 {
			t.Fatal(n, err)
		}
		runs, err := h.Store.ListRuns("alice", 100)
		if err != nil || len(runs) != 1 || !sourceScopeEqual(runs[0], task.Sources) {
			t.Fatal(runs, err)
		}
		p := policies["weather"]
		p.GrantedCapabilities = map[string]bool{}
		policies["weather"] = p
		now = 1120.
		if n, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || n != 1 {
			t.Fatal(n, err)
		}
		task, err = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
		if err != nil || task.Status != "paused" || task.LastExecution.ErrorCode != "capability_not_granted" {
			t.Fatal("revoked source schedule dispatched", task, err)
		}
	})
}

func TestProjectRetryAndPollingRecheckLivePermissions(t *testing.T) {
	for _, mode := range []string{"retry", "poll"} {
		t.Run(mode, func(t *testing.T) {
			model := &hostModel{decisions: []Decision{callDecision("weather::query")}}
			h, providers, policies := projectTestHost(t, model)
			now := unixNow()
			h.Clock = func() float64 { return now }
			p := providers["weather"]
			scope := []string{"query"}
			if mode == "retry" {
				p.hook = func(context.Context, string, JSON, *InvocationContext) (CapabilityResult, error) {
					return CapabilityResult{ErrorCode: "provider_outcome_unknown"}, nil
				}
			} else {
				cap := p.caps["query"]
				cap.Operation = &OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "status", PollArgument: []string{"id"}, PendingStates: []string{"queued"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}, IntervalSeconds: 1, TimeoutSeconds: 3600}
				p.caps["query"] = cap
				p.caps["status"] = CapabilityDescription{Name: "status", Version: "1", Effect: "read", Replay: "safe", ReferenceScope: "durable", InputSchema: JSON{"type": "object"}}
				root := policies["home"]
				root.Delegations["weather"] = []string{"query", "status"}
				policies["home"] = root
				target := policies["weather"]
				target.GrantedCapabilities["status"] = true
				policies["weather"] = target
				scope = append(scope, "status")
				p.hook = func(_ context.Context, name string, _ JSON, inv *InvocationContext) (CapabilityResult, error) {
					if inv.TargetSubject != "alice-weather" {
						t.Error("poll lost target identity")
					}
					return CapabilityResult{Data: JSON{"id": "job-1", "status": "queued"}, ReferenceScope: "durable"}, nil
				}
			}
			run, err := h.CreateWithSources(t.Context(), "alice", "home", "Weather", "", []RunSource{source("weather", scope...)})
			if err != nil {
				t.Fatal(err)
			}
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "waiting" || p.calls != 1 {
				t.Fatal("first invocation", mode, run.Status, err, p.calls)
			}
			target := policies["weather"]
			if mode == "retry" {
				target.GrantedCapabilities["query"] = false
			} else {
				target.GrantedCapabilities["status"] = false
			}
			policies["weather"] = target
			now += 10
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if run.Status != "needs_authorization" || p.calls != 1 {
				t.Fatal("revoked retry/poll executed", mode, run.Status, err, p.calls)
			}
		})
	}
}

func TestProjectVerifiedCredentialsArePerUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.URL.Path == "/me" {
			writeJSON(w, 200, JSON{"sub": token, "capabilities": []string{"query"}})
		} else {
			writeJSON(w, 200, JSON{"account": token})
		}
	}))
	defer srv.Close()
	dep := &Deployment{Config: DeploymentConfig{Packs: map[string]PackConfig{"b": {Path: writeProjectPack(t, t.TempDir(), "b")}}, Users: map[string]UserConfig{}}, Environment: map[string]string{"URL": srv.URL, "ME": srv.URL + "/me", "ALICE_TOKEN": "alice-b", "BOB_TOKEN": "bob-b"}}
	for _, owner := range []string{"alice", "bob"} {
		ref := strings.ToUpper(owner) + "_TOKEN"
		dep.Config.Users[owner] = UserConfig{Packs: map[string]ConnectionConfig{"b": {Environment: map[string]string{"API_URL": "URL", "API_TOKEN": ref}, AllowModelData: true, GrantedCapabilities: []string{"query"}, Identity: &IdentityConfig{URLEnv: "ME", TokenEnv: ref, ExpectedSubject: owner + "-b", CapabilitiesPath: []any{"capabilities"}}}}}
		provider, err := dep.ProviderFactory(t.Context(), owner, "b")
		if err != nil {
			t.Fatal(err)
		}
		if provider.(interface{ BoundSubject() string }).BoundSubject() != owner+"-b" {
			t.Fatal("credential identity mismatched")
		}
		result, err := provider.Invoke(t.Context(), "query", JSON{}, nil)
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
		if err != nil || result.Data["account"] != owner+"-b" {
			t.Fatal("user connection crossed account boundary", owner, result, err)
		}
	}
}

func TestProjectSourceSkillsDisappearAfterRevocation(t *testing.T) {
	h, providers, policies := projectTestHost(t, &hostModel{})
	p := providers["weather"]
	p.skills = map[string]Skill{"usage": {Description: SkillDescription{Name: "usage", Description: "Weather API usage"}, Content: "Use metric units for weather calls."}}
	cap := p.caps["query"]
	cap.SkillsList = []string{"usage"}
	p.caps["query"] = cap
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "", []RunSource{source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := h.openRunProvider(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(provider.Close)
	policy, err := h.projectPolicy(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &AgentRuntime{Provider: provider, Grants: policy.GrantedCapabilities}
	state, callErr8 := runtime.NewState("weather", run.RunID)
	if callErr8 != nil {
		t.Error(callErr8)
	}
	state.LoadedSkills = []string{"weather::usage", "weather::$project"}
	packet := runtime.Context(state)
	if len(packet.LoadedSkills) != 2 {
		t.Fatal("source skills not namespaced", packet)
	}
	target := policies["weather"]
	target.GrantedCapabilities = map[string]bool{}
	policies["weather"] = target
	policy, err = h.projectPolicy(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Grants = policy.GrantedCapabilities
	packet = runtime.Context(state)
	if len(packet.LoadedSkills) != 0 || len(packet.Skills) != 0 {
		t.Fatal("revoked source instructions still exposed", packet)
	}
	runtime.Model = &hostModel{decisions: []Decision{{Schema: "agenstra.decision.v1", Kind: "read_skill", Name: "weather::usage"}}}
	if err = runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Observations[len(state.Observations)-1].ErrorCode == nil || *state.Observations[len(state.Observations)-1].ErrorCode != "skill_unknown" {
		t.Fatal("revoked source skill could be reloaded")
	}
}

func TestProjectQualifiedDecisionNames(t *testing.T) {
	name := strings.Repeat("p", 128) + "::" + strings.Repeat("c", 128)
	decision := callDecision(name)
	if err := decision.Validate(); err != nil {
		t.Fatal("qualified valid names rejected", err)
	}
	if err := (Decision{Kind: "inspect_capability", Name: name}).Validate(); err != nil {
		t.Fatal("qualified inspection name rejected", err)
	}
	decision.Calls[0].Capability = strings.Repeat("x", 331)
	if err := decision.Validate(); err == nil {
		t.Fatal("unbounded qualified name accepted")
	}
}

func TestProjectCredentialAttestationUsesLoadedProvider(t *testing.T) {
	token := "USER_TOKEN"
	c := ConnectionConfig{Environment: map[string]string{"USER_TOKEN": "ALICE_TOKEN"}, Identity: &IdentityConfig{TokenEnv: "ALICE_TOKEN", ExpectedSubject: "alice-b", CapabilitiesPath: []any{"capabilities"}}}
	rest := &RestPack{Manifest: RestManifest{TokenEnv: &token}}
	if subject := credentialSubject(rest, "alice", c); subject != "alice-b" {
		t.Fatal(subject)
	}
	rest.Manifest.HeadersEnv = map[string]string{"X-API-Key": "OTHER_TOKEN"}
	if subject := credentialSubject(rest, "alice", c); subject != "" {
		t.Fatal("mixed credential headers attested", subject)
	}
	mcp := &MCPPack{Manifest: MCPManifest{Source: MCPSource{Transport: "streamable_http", TokenEnv: &token}}}
	if subject := credentialSubject(mcp, "alice", c); subject != "alice-b" {
		t.Fatal(subject)
	}
	mcp.Manifest.Source.Transport = "stdio"
	if subject := credentialSubject(mcp, "alice", c); subject != "" {
		t.Fatal("unverifiable stdio identity accepted", subject)
	}
}

func TestConfiguredReconcilerWithSources(t *testing.T) {
	for _, verifier := range []string{"records.verify", "weather::query"} {
		t.Run(verifier, func(t *testing.T) {
			h, providers, policies := projectTestHost(t, &hostModel{decisions: []Decision{callDecision("job.submit")}})
			read := CapabilityDescription{Name: "records.verify", Version: "1", Effect: "read", InputSchema: JSON{"type": "object"}}
			providers["home"].caps[read.Name] = read
			providers["home"].caps["job.submit"] = CapabilityDescription{Name: "job.submit", Version: "1", Effect: "write", Replay: "never", InputSchema: JSON{"type": "object"}, OutputSchema: JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}}}
			policies["home"].GrantedCapabilities[read.Name], policies["home"].GrantedCapabilities["job.submit"] = true, true
			writes, reads := 0, 0
			originalID := ""
			check := func(target string) func(context.Context, string, JSON, *InvocationContext) (CapabilityResult, error) {
				return func(_ context.Context, name string, args JSON, inv *InvocationContext) (CapabilityResult, error) {
					if name == "job.submit" {
						writes++
						return CapabilityResult{}, errors.New("response lost")
					}
					reads++
					if inv == nil || inv.OwnerID != "alice" || inv.RunID == "" || inv.OriginPackID != "home" || inv.TargetPackID != target || inv.TargetSubject != "alice-"+target || inv.InvocationID == originalID || args["request_id"] != originalID {
						t.Error("verifier identity or original request binding lost", inv, args)
					}
					return CapabilityResult{Data: JSON{"status": "done", "result": JSON{"id": "R-1"}}}, nil
				}
			}
			providers["home"].hook, providers["weather"].hook = check("home"), check("weather")
			d := &Deployment{Config: DeploymentConfig{ReconciliationChecks: map[string]map[string]ReconciliationRule{"home": {"job.submit": {VerifyCapability: verifier, Arguments: map[string][]any{"request_id": {"invocation_id"}}, SuccessPath: []any{"status"}, SuccessValue: "done", ResultPath: []any{"result"}}}}}}
			if err := configureDeploymentChecks(h, d); err != nil {
				t.Fatal(err)
			}
			run, err := h.CreateWithSources(t.Context(), "alice", "home", "verify the original operation", "", []RunSource{source("weather", "query")})
			if err != nil {
				t.Fatal(err)
			}
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "needs_reconciliation" {
				t.Fatal(run.Status, err)
			}
			state, err := h.restore(run)
			if err != nil || len(state.Pending) != 1 {
				t.Fatal(state, err)
			}
			item := state.Pending[0]
			originalID = item.InvocationID
			settled, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
			if err != nil || settled.Status != "queued" || writes != 1 || reads != 1 {
				t.Fatal(settled.Status, err, writes, reads)
			}
			retry, err := h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
			if err != nil || retry.Revision != settled.Revision || writes != 1 || reads != 1 {
				t.Fatal("reconciliation replayed work", retry, err, writes, reads)
			}
		})
	}
}
