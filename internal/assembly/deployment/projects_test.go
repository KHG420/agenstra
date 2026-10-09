package deployment

import (
	"context"
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
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

func TestProjectCredentialAttestationUsesLoadedProvider(t *testing.T) {
	token := "USER_TOKEN"
	c := agentcontract.ConnectionConfig{Environment: map[string]string{"USER_TOKEN": "ALICE_TOKEN"}, Identity: &agentcontract.IdentityConfig{TokenEnv: "ALICE_TOKEN", ExpectedSubject: "alice-b", CapabilitiesPath: []any{"capabilities"}}}
	rest := &capabilitypack.RestPack{Manifest: agentcontract.RestManifest{TokenEnv: &token}}
	if subject := credentialSubject(rest, "alice", c); subject != "alice-b" {
		t.Fatal(subject)
	}
	rest.Manifest.HeadersEnv = map[string]string{"X-API-Key": "OTHER_TOKEN"}
	if subject := credentialSubject(rest, "alice", c); subject != "" {
		t.Fatal("mixed credential headers attested", subject)
	}
	mcp := &capabilitypack.MCPPack{Manifest: agentcontract.MCPManifest{Source: agentcontract.MCPSource{Transport: "streamable_http", TokenEnv: &token}}}
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
			h, providers, policies := projectTestHost(t, &hostModel{decisions: []agentcontract.Decision{callDecision("job.submit")}})
			read := agentcontract.CapabilityDescription{Name: "records.verify", Version: "1", Effect: "read", InputSchema: agentcontract.JSON{"type": "object"}}
			providers["home"].caps[read.Name] = read
			providers["home"].caps["job.submit"] = agentcontract.CapabilityDescription{Name: "job.submit", Version: "1", Effect: "write", Replay: "never", InputSchema: agentcontract.JSON{"type": "object"}, OutputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}}}
			policies["home"].GrantedCapabilities[read.Name], policies["home"].GrantedCapabilities["job.submit"] = true, true
			writes, reads := 0, 0
			originalID := ""
			check := func(target string) func(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
				return func(_ context.Context, name string, args agentcontract.JSON, inv *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
					if name == "job.submit" {
						writes++
						return agentcontract.CapabilityResult{}, errors.New("response lost")
					}
					reads++
					if inv == nil || inv.OwnerID != "alice" || inv.RunID == "" || inv.OriginPackID != "home" || inv.TargetPackID != target || inv.TargetSubject != "alice-"+target || inv.InvocationID == originalID || args["request_id"] != originalID {
						t.Error("verifier identity or original request binding lost", inv, args)
					}
					return agentcontract.CapabilityResult{Data: agentcontract.JSON{"status": "done", "result": agentcontract.JSON{"id": "R-1"}}}, nil
				}
			}
			providers["home"].hook, providers["weather"].hook = check("home"), check("weather")
			d := &Deployment{Config: agentcontract.DeploymentConfig{ReconciliationChecks: map[string]map[string]agentcontract.ReconciliationRule{"home": {"job.submit": {VerifyCapability: verifier, Arguments: map[string][]any{"request_id": {"invocation_id"}}, SuccessPath: []any{"status"}, SuccessValue: "done", ResultPath: []any{"result"}}}}}}
			if err := ConfigureDeploymentChecks(h, d); err != nil {
				t.Fatal(err)
			}
			run, err := h.CreateWithSources(t.Context(), "alice", "home", "verify the original operation", "", []agentcontract.RunSource{source("weather", "query")})
			if err != nil {
				t.Fatal(err)
			}
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "needs_reconciliation" {
				t.Fatal(run.Status, err)
			}
			state, err := h.Restore(run)
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
