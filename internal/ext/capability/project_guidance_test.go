package capability

import (
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestPackageProjectGuidancePreservesOnlyOriginalInstructions(t *testing.T) {
	for _, guidance := range []string{"", "Keep business JSON {\"schema\":\"invoice\",\"kind\":\"paid\"} unchanged.\nDo not expand grants."} {
		providers := []interface {
			ProjectGuidance() string
			SystemPrompt() string
		}{
			&RestPack{Manifest: agentcontract.RestManifest{Guidance: guidance}},
			&MCPPack{Manifest: agentcontract.MCPManifest{Guidance: guidance}},
			&LegacyRestPack{Manifest: agentcontract.LegacyPackManifest{Guidance: guidance}},
		}
		for _, provider := range providers {
			if provider.ProjectGuidance() != guidance || !strings.Contains(provider.SystemPrompt(), guidance) || !strings.Contains(provider.SystemPrompt(), "agenstra.decision.v1") {
				t.Fatal("scoped guidance was edited or standalone protocol changed")
			}
		}
	}
}
