package service

import (
	"context"
	"fmt"
	"slices"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestChatRetains104AuthorizedSourceCapabilitiesAndBoundedModelCatalog(t *testing.T) {
	model := &hostModel{hook: func(packet agentcontract.ContextPacket) {
		visible := 0
		for _, capability := range packet.Capabilities {
			if capability["input_schema"] != nil {
				visible++
			}
		}
		if visible > 8 {
			t.Errorf("larger task scope forced all contracts into model context: %d", visible)
		}
	}}
	f := newWebFixture(t, model, false)
	f.h.Settings.MaxContextCapabilities = 8
	names := make([]string, 104)
	caps := map[string]agentcontract.CapabilityDescription{}
	grants := map[string]bool{}
	for i := range names {
		names[i] = fmt.Sprintf("records.read.%03d", i)
		caps[names[i]] = agentcontract.CapabilityDescription{Name: names[i], Description: "Read records", Version: "1",
			Effect: "read", Replay: "safe", InputSchema: agentcontract.JSON{"type": "object"}}
		grants[names[i]] = true
	}
	provider := &projectTestProvider{hostProvider: hostProvider{caps: caps}, subject: "alice-records"}
	originalPolicy, originalFactory := f.w.basePolicy, f.w.baseFactory
	f.w.basePolicy = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
		if pack == "other" {
			return agentcontract.ExecutionPolicy{AllowModelData: true, PermissionsVerified: true, Subject: "alice-records", GrantedCapabilities: grants}, nil
		}
		policy, err := originalPolicy(ctx, owner, pack)
		policy.Delegations = map[string][]string{"other": slices.Clone(names)}
		return policy, err
	}
	f.w.baseFactory = func(ctx context.Context, owner, pack string) (agentcontract.CapabilityProvider, error) {
		if pack == "other" {
			return provider, nil
		}
		return originalFactory(ctx, owner, pack)
	}
	conversation, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	sources := []agentcontract.RunSource{{PackID: "other", Capabilities: slices.Clone(names)}}
	message, err := f.w.SubmitMessageWithSources(t.Context(), "alice", conversation.ID, "large-scope", "Report ready; do not call business APIs", "", sources)
	if err != nil {
		t.Fatal("chat rejected the complete 104-capability authorized source", err)
	}
	if err := f.w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	run, err := f.h.Drive(t.Context(), message.RunID, "alice")
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	model.mu.Lock()
	modelCalls := model.calls
	model.mu.Unlock()
	if err != nil || run.Status != "completed" || !agentcontract.SourceScopeEqual(run, sources) || calls != 0 || modelCalls == 0 {
		t.Fatal("chat lost frozen source scope, skipped the model or executed an unwanted business call", err, calls, modelCalls)
	}
	bindings, err := agentcontract.RunBindings(run)
	if err != nil || len(bindings) != 1 || bindings[0].Subject != "alice-records" || len(bindings[0].Capabilities) != 104 {
		t.Fatal("chat did not freeze the full scope and verified target subject", bindings, err)
	}
	delete(grants, names[103])
	if _, err := f.w.SubmitMessageWithSources(t.Context(), "alice", conversation.ID, "revoked", "Ready", "", sources); agentcontract.ErrorCode(err) != "capability_not_granted" {
		t.Fatal("a larger source scope bypassed target grant revocation", err)
	}
	if _, err := f.w.SubmitMessageWithSources(t.Context(), "bob", conversation.ID, "other-owner", "Ready", "", sources); err == nil {
		t.Fatal("a larger source scope bypassed conversation ownership")
	}
}
