package deployment

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
)

func TestBoundProviderRetainsConcurrency(t *testing.T) {
	var provider agentcontract.CapabilityProvider = &boundProvider{CapabilityProvider: &capabilitypack.RestPack{}}
	concurrent, ok := provider.(agentcontract.ConcurrentCapabilityProvider)
	if !ok || !concurrent.ConcurrentInvocation("records.query") {
		t.Fatal("deployment wrapper removed the REST provider's concurrency capability")
	}
}
