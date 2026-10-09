package host

import (
	"context"
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

type projectTestProvider struct {
	hostProvider
	subject string
	skills  map[string]agentcontract.Skill
}

func (p *projectTestProvider) BoundSubject() string { return p.subject }

func (p *projectTestProvider) Skills() map[string]agentcontract.Skill { return p.skills }

func projectTestHost(t *testing.T, model agentcontract.DecisionModel) (*AgentHost, map[string]*projectTestProvider, map[string]agentcontract.ExecutionPolicy) {
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
	h := NewAgentHost(testStore(t), func(_ context.Context, owner, pack string) (agentcontract.CapabilityProvider, error) {
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

func TestProjectTaskScopeDeniesAdminReadsAndExplicitReads(t *testing.T) {
	model := &hostModel{decisions: []agentcontract.Decision{callDecision("weather::admin.read")}}
	h, providers, _ := projectTestHost(t, model)
	model.hook = func(p agentcontract.ContextPacket) {
		for _, c := range p.Capabilities {
			if c["name"] == "weather::admin.read" || c["name"] == "admin.read" {
				t.Fatal("ungranted admin read exposed", c)
			}
		}
	}
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "Weather", "scope", []agentcontract.RunSource{source("weather", "query")})
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
	if _, err = h.CreateWithSources(t.Context(), "alice", "home", "Weather", "admin", []agentcontract.RunSource{source("weather", "admin.read")}); agentcontract.ErrorCode(err) != "capability_not_granted" {
		t.Fatal("admin authority escaped delegation ceiling", err)
	}
	p := providers["home"]
	outcome, err := reactcore.ExecuteCall(t.Context(), p, nil, agentcontract.ToolCall{Capability: "query", Arguments: agentcontract.JSON{}}, nil)
	if err != nil || outcome.ErrorCode != "capability_not_granted" || p.calls != 0 {
		t.Fatal("ungranted read executed", outcome, err)
	}
	runtime := &reactcore.AgentRuntime{Provider: p, Grants: map[string]bool{}}
	state, callErr := runtime.NewState("inspect", "inspect")
	if callErr != nil {
		t.Error(callErr)
	}
	runtime.Model = &hostModel{decisions: []agentcontract.Decision{{Schema: "agenstra.decision.v1", Kind: "inspect_capability", Name: "admin.read"}}}
	if err := runtime.Step(t.Context(), state, nil); err != nil || state.InspectedCapability != nil {
		t.Fatal("ungranted read inspection exposed", err)
	}
}

func TestProjectRevocationBeforeInvocationAndIdentityBinding(t *testing.T) {
	for _, revoke := range []string{"delegation", "target_permission", "subject", "credential_subject", "missing_verification"} {
		t.Run(revoke, func(t *testing.T) {
			model := &hostModel{decisions: []agentcontract.Decision{callDecision("weather::query")}}
			h, providers, policies := projectTestHost(t, model)
			run, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "", []agentcontract.RunSource{source("weather", "query")})
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
				model.hook = func(agentcontract.ContextPacket) { mutate() }
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
	h.ReleaseProviderFactory = func(_ context.Context, owner, pack, version string) (agentcontract.CapabilityProvider, error) {
		opened[pack] = version
		return providers[pack], nil
	}
	for _, item := range []struct{ owner, pack, key, value string }{
		{"alice", "home", "report.language", "en"}, {"alice", "weather", "report.language", "zh-CN"}, {"alice", "weather", "query.units", "metric"},
	} {
		if _, err := h.SetMemory(t.Context(), item.owner, item.pack, agentcontract.MemoryUpdate{Scope: "pack", Key: item.key, Value: item.value, Kind: "preference"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Store.SetMemory("bob", "home", agentcontract.MemoryUpdate{Scope: "pack", Key: "report.language", Value: "fr", Kind: "preference"}); err != nil {
		t.Fatal(err)
	}
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "stable", []agentcontract.RunSource{source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	release = "v2"
	repeated, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "stable", []agentcontract.RunSource{source("weather", "query")})
	if err != nil || repeated.RunID != run.RunID {
		t.Fatal(err)
	}
	if _, err = h.Create(t.Context(), "alice", "home", "weather", "stable"); agentcontract.ErrorCode(err) != "request_id_conflict" {
		t.Fatal("replay dropped source scope", err)
	}
	if _, err = h.CreateWithSources(t.Context(), "alice", "home", "weather", "stable", []agentcontract.RunSource{source("location", "query")}); agentcontract.ErrorCode(err) != "request_id_conflict" {
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
	model := &hostModel{decisions: []agentcontract.Decision{callDecision("weather::query")}}
	h, providers, policies := projectTestHost(t, model)
	cap := providers["weather"].caps["query"]
	cap.ApprovalRequired = true
	providers["weather"].caps["query"] = cap
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "", []agentcontract.RunSource{source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_approval" {
		t.Fatal(run.Status, err)
	}
	state, callErr3 := h.Restore(run)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	item := state.Pending[0]
	policy := policies["weather"]
	policy.GrantedCapabilities = map[string]bool{}
	policies["weather"] = policy
	if _, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true); agentcontract.ErrorCode(err) != "capability_not_granted" {
		t.Fatal("approval enabled revoked grant", err)
	}
	if err = pollAccess(providers["weather"], policy, agentcontract.OperationBinding{PollCapability: "query"}); agentcontract.ErrorCode(err) != "capability_not_granted" {
		t.Fatal("poll bypassed read grant", err)
	}
}

func TestProjectSourceValidationAndOwnerIsolation(t *testing.T) {
	h, _, _ := projectTestHost(t, &hostModel{})
	for _, sources := range [][]agentcontract.RunSource{{source("home", "query")}, {source("weather", "query", "query")}, {source("weather", "other::query")}, {source("weather", "query"), source("weather", "query")}, {source("weather")}} {
		if _, err := h.CreateWithSources(t.Context(), "alice", "home", "query", "", sources); agentcontract.ErrorCode(err) != "source_scope_invalid" {
			t.Fatal(sources, err)
		}
	}
	if _, err := h.CreateWithSources(t.Context(), "bob", "home", "query", "", []agentcontract.RunSource{source("weather", "query")}); agentcontract.ErrorCode(err) != "access_denied" {
		t.Fatal("another owner's account reused", err)
	}
	if _, err := h.CreateWithSources(t.Context(), "alice", "home", "query", "", []agentcontract.RunSource{source("unknown", "query")}); agentcontract.ErrorCode(err) != "access_denied" {
		t.Fatal("missing account mapping trusted", err)
	}
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "query", "", []agentcontract.RunSource{source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Get(t.Context(), run.RunID, "bob"); !errors.Is(err, runstore.ErrRunNotFound) {
		t.Fatal("cross task ownership lost", err)
	}
}

func TestProjectRetryAndPollingRecheckLivePermissions(t *testing.T) {
	for _, mode := range []string{"retry", "poll"} {
		t.Run(mode, func(t *testing.T) {
			model := &hostModel{decisions: []agentcontract.Decision{callDecision("weather::query")}}
			h, providers, policies := projectTestHost(t, model)
			now := runstore.UnixNow()
			h.Clock = func() float64 { return now }
			p := providers["weather"]
			scope := []string{"query"}
			if mode == "retry" {
				p.hook = func(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
					return agentcontract.CapabilityResult{ErrorCode: "provider_outcome_unknown"}, nil
				}
			} else {
				cap := p.caps["query"]
				cap.Operation = &agentcontract.OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "status", PollArgument: []string{"id"}, PendingStates: []string{"queued"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}, IntervalSeconds: 1, TimeoutSeconds: 3600}
				p.caps["query"] = cap
				p.caps["status"] = agentcontract.CapabilityDescription{Name: "status", Version: "1", Effect: "read", Replay: "safe", ReferenceScope: "durable", InputSchema: agentcontract.JSON{"type": "object"}}
				root := policies["home"]
				root.Delegations["weather"] = []string{"query", "status"}
				policies["home"] = root
				target := policies["weather"]
				target.GrantedCapabilities["status"] = true
				policies["weather"] = target
				scope = append(scope, "status")
				p.hook = func(_ context.Context, name string, _ agentcontract.JSON, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
					if inv.TargetSubject != "alice-weather" {
						t.Error("poll lost target identity")
					}
					return agentcontract.CapabilityResult{Data: agentcontract.JSON{"id": "job-1", "status": "queued"}, ReferenceScope: "durable"}, nil
				}
			}
			run, err := h.CreateWithSources(t.Context(), "alice", "home", "Weather", "", []agentcontract.RunSource{source("weather", scope...)})
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

func TestProjectSourceSkillsDisappearAfterRevocation(t *testing.T) {
	h, providers, policies := projectTestHost(t, &hostModel{})
	p := providers["weather"]
	p.skills = map[string]agentcontract.Skill{"usage": {Description: agentcontract.SkillDescription{Name: "usage", Description: "Weather API usage"}, Content: "Use metric units for weather calls."}}
	cap := p.caps["query"]
	cap.SkillsList = []string{"usage"}
	p.caps["query"] = cap
	run, err := h.CreateWithSources(t.Context(), "alice", "home", "weather", "", []agentcontract.RunSource{source("weather", "query")})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := h.OpenRunProvider(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(provider.Close)
	policy, err := h.ProjectPolicy(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &reactcore.AgentRuntime{Provider: provider, Grants: policy.GrantedCapabilities}
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
	policy, err = h.ProjectPolicy(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Grants = policy.GrantedCapabilities
	packet = runtime.Context(state)
	if len(packet.LoadedSkills) != 0 || len(packet.Skills) != 0 {
		t.Fatal("revoked source instructions still exposed", packet)
	}
	runtime.Model = &hostModel{decisions: []agentcontract.Decision{{Schema: "agenstra.decision.v1", Kind: "read_skill", Name: "weather::usage"}}}
	if err = runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Observations[len(state.Observations)-1].ErrorCode == nil || *state.Observations[len(state.Observations)-1].ErrorCode != "skill_unknown" {
		t.Fatal("revoked source skill could be reloaded")
	}
}
