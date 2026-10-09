package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
)

type projectTestProvider struct {
	hostProvider
	subject string
	skills  map[string]agentcontract.Skill
}

func (p *projectTestProvider) BoundSubject() string { return p.subject }

func (p *projectTestProvider) Skills() map[string]agentcontract.Skill { return p.skills }

func projectTestHost(t *testing.T, model agentcontract.DecisionModel) (*durablehost.AgentHost, map[string]*projectTestProvider, map[string]agentcontract.ExecutionPolicy) {
	t.Helper()
	providers := map[string]*projectTestProvider{}
	policies := map[string]agentcontract.ExecutionPolicy{}
	for _, pack := range []string{"home", "location", "weather"} {
		providers[pack] = &projectTestProvider{hostProvider: hostProvider{caps: map[string]agentcontract.CapabilityDescription{
			"query":      {Name: "query", Version: "1", Effect: "read", Replay: "safe", InputSchema: agentcontract.JSON{"type": "object"}, ReferenceScope: "durable"},
			"admin.read": {Name: "admin.read", Version: "1", Effect: "read", Replay: "safe", InputSchema: agentcontract.JSON{"type": "object"}},
		}, binding: pack}, subject: "alice-" + pack}
		policies[pack] = agentcontract.ExecutionPolicy{AllowModelData: true, Subject: "alice-" + pack, PermissionsVerified: true, GrantedCapabilities: map[string]bool{"query": true, "admin.read": true}}
	}
	root := policies["home"]
	root.GrantedCapabilities = map[string]bool{"query": true}
	root.Delegations = map[string][]string{"location": {"query"}, "weather": {"query"}}
	policies["home"] = root
	h := durablehost.NewAgentHost(testStore(t), func(_ context.Context, owner, pack string) (agentcontract.CapabilityProvider, error) {
		if owner != "alice" || providers[pack] == nil {
			return nil, agentcontract.NewHostError("access_denied")
		}
		return providers[pack], nil
	}, model, func(_ context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
		if owner != "alice" {
			return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("access_denied")
		}
		p, ok := policies[pack]
		if !ok {
			return p, agentcontract.NewHostError("access_denied")
		}
		return p, nil
	})
	return h, providers, policies
}

func source(pack string, names ...string) agentcontract.RunSource {
	return agentcontract.RunSource{PackID: pack, Capabilities: names}
}

func writeProjectPack(t *testing.T, dir, pack string) string {
	t.Helper()
	cap := agentcontract.JSON{"name": "query", "description": "Query " + pack, "method": "GET", "path": "/query", "effect": "read", "input_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"query": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"city": agentcontract.JSON{"type": "string"}}, "additionalProperties": false}}, "additionalProperties": false}, "output_schema": agentcontract.JSON{"type": "object"}}
	admin := agentcontract.JSON{"name": "admin.read", "description": "Read admin secrets", "method": "GET", "path": "/admin", "effect": "read", "input_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false}, "output_schema": agentcontract.JSON{"type": "object"}}
	raw, callErr4 := json.Marshal(agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": pack, "version": "1.0.0", "guidance": "Use only this project's query conventions.", "base_url_env": "API_URL", "token_env": "API_TOKEN", "capabilities": []any{cap, admin}})
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
	dep := &deployassembly.Deployment{Config: agentcontract.DeploymentConfig{Packs: map[string]agentcontract.PackConfig{}, Users: map[string]agentcontract.UserConfig{}}, Environment: map[string]string{}}
	connections := map[string]agentcontract.ConnectionConfig{}
	for _, pack := range []string{"home", "location", "weather"} {
		name := pack
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer alice-"+name+"-token" {
				w.WriteHeader(403)
				return
			}
			if r.URL.Path == "/me" {
				writeJSON(w, 200, agentcontract.JSON{"sub": "account-alice-" + name, "capabilities": []string{"query", "admin.read"}})
				return
			}
			if r.URL.Path == "/admin" {
				t.Error("administrator API executed")
				w.WriteHeader(403)
				return
			}
			if name == "location" {
				locationCalls.Add(1)
				writeJSON(w, 200, agentcontract.JSON{"city": "Shanghai"})
				return
			}
			if name == "weather" {
				weatherCalls.Add(1)
				if r.URL.Query().Get("city") != "Shanghai" {
					t.Error("location Fact reference was not forwarded")
				}
				writeJSON(w, 200, agentcontract.JSON{"forecast": "sunny"})
				return
			}
			t.Error("home backend called unexpectedly")
		}))
		t.Cleanup(srv.Close)
		dep.Config.Packs[name] = agentcontract.PackConfig{Path: writeProjectPack(t, dir, name)}
		prefix := strings.ToUpper(name)
		dep.Environment[prefix+"_URL"], dep.Environment[prefix+"_ME"], dep.Environment[prefix+"_TOKEN"] = srv.URL, srv.URL+"/me", "alice-"+name+"-token"
		connections[name] = agentcontract.ConnectionConfig{Environment: map[string]string{"API_URL": prefix + "_URL", "API_TOKEN": prefix + "_TOKEN"}, GrantedCapabilities: []string{"query", "admin.read"}, AllowModelData: true, Identity: &agentcontract.IdentityConfig{URLEnv: prefix + "_ME", TokenEnv: prefix + "_TOKEN", ExpectedSubject: "account-alice-" + name, CapabilitiesPath: []any{"capabilities"}}}
	}
	home := connections["home"]
	home.GrantedCapabilities = []string{}
	home.Delegations = map[string][]string{"location": {"query"}, "weather": {"query"}}
	connections["home"] = home
	dep.Config.Users["alice"] = agentcontract.UserConfig{Packs: connections}
	rounds := 0
	model := contextBudgetModel(func(_ context.Context, p agentcontract.ContextPacket, _ string) (agentcontract.Decision, error) {
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
			return agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "location", Capability: "location::query", Arguments: agentcontract.JSON{}, Reason: "Find location"}}}, nil
		case 2:
			return agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{CallRef: "weather", Capability: "weather::query", Arguments: agentcontract.JSON{"query": agentcontract.JSON{"city": agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": p.Facts[0].FactID, "path": []any{"data", "city"}}}}}, Reason: "Query weather at user's location"}}}, nil
		default:
			ids := []string{}
			for _, f := range p.Facts {
				ids = append(ids, f.FactID)
			}
			return agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Shanghai: sunny", FactIDs: ids}, nil
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
	h := durablehost.NewAgentHost(testStore(t), dep.ProviderFactory, model, dep.PolicyResolver)
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "Today's weather", "", []agentcontract.RunSource{source("location", "query"), source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatal(run.Status, run.State, err)
	}
	state, callErr5 := h.Restore(run)
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
					writeJSON(w, 200, agentcontract.JSON{"sub": "admin", "capabilities": []string{"query"}})
				case "malformed_permissions":
					writeJSON(w, 200, agentcontract.JSON{"sub": "alice-b", "capabilities": []any{42}})
				case "empty_permissions":
					writeJSON(w, 200, agentcontract.JSON{"sub": "alice-b", "capabilities": []any{}})
				default:
					writeJSON(w, 200, agentcontract.JSON{"sub": "alice-b", "capabilities": []string{"query"}})
				}
			}))
			defer srv.Close()
			c := agentcontract.ConnectionConfig{Environment: map[string]string{"API_URL": "URL", "API_TOKEN": "TOKEN"}, GrantedCapabilities: []string{"query"}, AllowModelData: true, Identity: &agentcontract.IdentityConfig{URLEnv: "ME", TokenEnv: "TOKEN", ExpectedSubject: "alice-b", CapabilitiesPath: []any{"capabilities"}}}
			if mode == "missing_permissions" {
				c.Identity.CapabilitiesPath = nil
			}
			if mode == "mismatched_token" {
				c.Environment["API_TOKEN"] = "ADMIN_TOKEN"
			}
			dep := &deployassembly.Deployment{Config: agentcontract.DeploymentConfig{Packs: map[string]agentcontract.PackConfig{"b": {Path: writeProjectPack(t, t.TempDir(), "b")}}, Users: map[string]agentcontract.UserConfig{"alice": {Packs: map[string]agentcontract.ConnectionConfig{"b": c}}}}, Environment: map[string]string{"URL": srv.URL, "ME": srv.URL, "TOKEN": "alice-token", "ADMIN_TOKEN": "admin-token"}}
			h, _, policies := projectTestHost(t, &hostModel{})
			h.PolicyResolver = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
				if pack == "home" {
					p := policies["home"]
					p.Delegations = map[string][]string{"b": {"query"}}
					return p, nil
				}
				return dep.PolicyResolver(ctx, owner, pack)
			}
			h.ProviderFactory = func(ctx context.Context, owner, pack string) (agentcontract.CapabilityProvider, error) {
				if pack == "home" {
					return &hostProvider{}, nil
				}
				return dep.ProviderFactory(ctx, owner, pack)
			}
			run, err := h.CreateWithSources(t.Context(), "alice", "home", "Query B", "", []agentcontract.RunSource{source("b", "query")})
			if mode == "mismatched_token" {
				if err != nil {
					t.Fatal(err)
				}
				run, err = h.Drive(t.Context(), run.RunID, "alice")
				if run.Status != "needs_authorization" || agentcontract.ErrorCode(err) != "identity_unverified" {
					t.Fatal("mismatched credentials trusted", run.Status, err)
				}
			} else if err == nil {
				t.Fatal("unverified authority accepted", mode)
			}
		})
	}
}

func TestProjectScopeAcrossHTTPChatBrowserAndSchedules(t *testing.T) {
	t.Run("HTTP", func(t *testing.T) {
		h, _, _ := projectTestHost(t, &hostModel{})
		dep := &deployassembly.Deployment{Config: agentcontract.DeploymentConfig{Users: map[string]agentcontract.UserConfig{"alice": {APIKeyEnv: "ALICE_KEY"}}}, Environment: map[string]string{"ALICE_KEY": "key"}}
		server, err := NewHTTPServer(h, dep, false, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func(close func() error) {
			if err := close(); err != nil {
				t.Error(err)
			}
		}(server.Close)
		request := func(body agentcontract.JSON) *httptest.ResponseRecorder {
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
		body := agentcontract.JSON{"pack_id": "home", "instruction": "query weather", "request_id": "cross-http", "sources": []agentcontract.RunSource{source("weather", "query")}}
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
			f := newWebFixture(t, &hostModel{decisions: []agentcontract.Decision{callDecision("weather::query")}}, false)
			originalPolicy, originalFactory := f.w.basePolicy, f.w.baseFactory
			provider := &projectTestProvider{hostProvider: hostProvider{}, subject: "alice-weather"}
			f.w.basePolicy = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
				if pack == "weather" {
					return agentcontract.ExecutionPolicy{AllowModelData: true, Subject: "alice-weather", PermissionsVerified: true, GrantedCapabilities: map[string]bool{"records.get": true, "query": true}}, nil
				}
				p, err := originalPolicy(ctx, owner, pack)
				p.Delegations = map[string][]string{"weather": {"records.get"}}
				return p, err
			}
			f.w.baseFactory = func(ctx context.Context, owner, pack string) (agentcontract.CapabilityProvider, error) {
				if pack == "weather" {
					return provider, nil
				}
				return originalFactory(ctx, owner, pack)
			}
			sources := []agentcontract.RunSource{source("weather", "records.get")}
			if mode == "chat" {
				c, err := f.w.CreateConversation(t.Context(), "alice", "records")
				if err != nil {
					t.Fatal(err)
				}
				m, err := f.w.SubmitMessageWithSources(t.Context(), "alice", c.ID, "one", "Weather", "", sources)
				if err != nil || !agentcontract.EqualSources(m.Sources, sources) {
					t.Fatal(m, err)
				}
				if _, err = f.w.SubmitMessage(t.Context(), "alice", c.ID, "one", "Weather", ""); agentcontract.ErrorCode(err) != "chat_message_conflict" {
					t.Fatal("chat replay dropped scope", err)
				}
				if err = f.w.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
				run, err := f.h.Get(t.Context(), m.RunID, "alice")
				if err != nil || !agentcontract.SourceScopeEqual(run, sources) {
					t.Fatal("chat did not persist scope", run, err)
				}
			} else {
				run, err := f.w.CreateBrowserRunWithSources(t.Context(), "alice", "records-web", f.session.ID, "Weather", "browser-cross", sources)
				if err != nil || !agentcontract.SourceScopeEqual(run, sources) {
					t.Fatal(run, err)
				}
				if _, err = f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Weather", "browser-cross"); agentcontract.ErrorCode(err) != "request_id_conflict" {
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
		task, err := h.CreateSchedule(t.Context(), "alice", agentcontract.ScheduleRequest{Name: "Weather", PackID: "home", Instruction: "Weather", Schedule: agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 60}, Sources: []agentcontract.RunSource{source("weather", "query")}})
		if err != nil {
			t.Fatal(err)
		}
		now = 1060.
		if n, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || n != 1 {
			t.Fatal(n, err)
		}
		runs, err := h.Store.ListRuns("alice", 100)
		if err != nil || len(runs) != 1 || !agentcontract.SourceScopeEqual(runs[0], task.Sources) {
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

func TestProjectVerifiedCredentialsArePerUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.URL.Path == "/me" {
			writeJSON(w, 200, agentcontract.JSON{"sub": token, "capabilities": []string{"query"}})
		} else {
			writeJSON(w, 200, agentcontract.JSON{"account": token})
		}
	}))
	defer srv.Close()
	dep := &deployassembly.Deployment{Config: agentcontract.DeploymentConfig{Packs: map[string]agentcontract.PackConfig{"b": {Path: writeProjectPack(t, t.TempDir(), "b")}}, Users: map[string]agentcontract.UserConfig{}}, Environment: map[string]string{"URL": srv.URL, "ME": srv.URL + "/me", "ALICE_TOKEN": "alice-b", "BOB_TOKEN": "bob-b"}}
	for _, owner := range []string{"alice", "bob"} {
		ref := strings.ToUpper(owner) + "_TOKEN"
		dep.Config.Users[owner] = agentcontract.UserConfig{Packs: map[string]agentcontract.ConnectionConfig{"b": {Environment: map[string]string{"API_URL": "URL", "API_TOKEN": ref}, AllowModelData: true, GrantedCapabilities: []string{"query"}, Identity: &agentcontract.IdentityConfig{URLEnv: "ME", TokenEnv: ref, ExpectedSubject: owner + "-b", CapabilitiesPath: []any{"capabilities"}}}}}
		provider, err := dep.ProviderFactory(t.Context(), owner, "b")
		if err != nil {
			t.Fatal(err)
		}
		if provider.(interface{ BoundSubject() string }).BoundSubject() != owner+"-b" {
			t.Fatal("credential identity mismatched")
		}
		result, err := provider.Invoke(t.Context(), "query", agentcontract.JSON{}, nil)
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
		if err != nil || result.Data["account"] != owner+"-b" {
			t.Fatal("user connection crossed account boundary", owner, result, err)
		}
	}
}
