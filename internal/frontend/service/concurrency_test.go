package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
)

type parallelTestProvider struct{ *hostProvider }

func (*parallelTestProvider) ConcurrentInvocation(string) bool { return true }

func concurrentDecision() agentcontract.Decision {
	d := agentcontract.Decision{Kind: "tool_batch"}
	for i := 0; i < 4; i++ {
		d.Calls = append(d.Calls, agentcontract.ToolCall{CallRef: fmt.Sprintf("read-%d", i), Capability: "records.get", Arguments: agentcontract.JSON{"id": fmt.Sprint(i)}, Reason: "Read independent record"})
	}
	return d
}

func concurrentHost(t *testing.T, p *parallelTestProvider) *durablehost.AgentHost {
	h := testHost(t, testStore(t), p.hostProvider, &hostModel{decisions: []agentcontract.Decision{concurrentDecision()}})
	h.ProviderFactory = func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return p, nil }
	h.Settings.MaxConcurrentTools = 2
	return h
}

func TestHostConcurrencySurvivesDeploymentAndBrowserWrappers(t *testing.T) {
	started, release := make(chan struct{}, 4), make(chan struct{})
	p := &parallelTestProvider{&hostProvider{hook: func(ctx context.Context, _ string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		started <- struct{}{}
		select {
		case <-release:
			return agentcontract.CapabilityResult{Data: args}, nil
		case <-ctx.Done():
			return agentcontract.CapabilityResult{}, ctx.Err()
		}
	}}}
	h := concurrentHost(t, p)
	wrapped := deployassembly.BindProvider(&browserProvider{base: deployassembly.BindProvider(p, "", ""), caps: p.Capabilities()}, "", "")
	h.ProviderFactory = func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return wrapped, nil }
	if wrapped.(agentcontract.ConcurrentCapabilityProvider).ConcurrentInvocation("ui.get_context") || wrapped.(agentcontract.ConcurrentCapabilityProvider).ConcurrentInvocation("ui.navigate") {
		t.Fatal("browser state became concurrent")
	}
	if deployassembly.BindProvider(&hostProvider{}, "", "").(agentcontract.ConcurrentCapabilityProvider).ConcurrentInvocation("records.get") {
		t.Fatal("serial provider gained concurrency")
	}
	run := createTestHostRun(t, h)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.Drive(ctx, run.RunID, "alice"); done <- err }()
	concurrent := true
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			concurrent = false
		}
	}
	close(release)
	err := <-done
	if !concurrent || err != nil {
		t.Fatal("wrapped business reads did not start concurrently", err)
	}
}
