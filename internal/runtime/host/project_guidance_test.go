package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type projectPromptTestProvider struct {
	*projectTestProvider
	prompt string
}

func (p *projectPromptTestProvider) SystemPrompt() string { return p.prompt }

type projectGuidanceTestProvider struct {
	*projectPromptTestProvider
	guidance string
}

func (p *projectGuidanceTestProvider) ProjectGuidance() string { return p.guidance }

func TestSourceProjectUsesScopedGuidanceWithoutDuplicatedProtocol(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available bool
		guidance  string
	}{
		{name: "business guidance", available: true, guidance: `Keep business {"schema":"invoice","kind":"paid"} unchanged.`},
		{name: "explicit empty guidance", available: true},
		{name: "custom provider fallback", guidance: "custom instructions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, providers, _ := projectTestHost(t, &hostModel{})
			const protocol = "GLOBAL_AGENT_OUTPUT_PROTOCOL\n"
			original := h.ProviderFactory
			h.ProviderFactory = func(ctx context.Context, owner, pack string) (agentcontract.CapabilityProvider, error) {
				provider, err := original(ctx, owner, pack)
				if err != nil || pack != "weather" {
					return provider, err
				}
				wrapped := &projectPromptTestProvider{projectTestProvider: providers[pack], prompt: protocol + tc.guidance}
				if tc.available {
					return &projectGuidanceTestProvider{projectPromptTestProvider: wrapped, guidance: tc.guidance}, nil
				}
				return wrapped, nil
			}
			run, err := h.CreateWithSources(t.Context(), "alice", "home", "Read weather", "", []agentcontract.RunSource{source("weather", "query")})
			if err != nil {
				t.Fatal(err)
			}
			provider, err := h.OpenRunProvider(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := provider.Close(); err != nil {
					t.Error(err)
				}
			}()
			want := tc.guidance
			if !tc.available {
				want = protocol + want
			}
			if provider.Skills()["weather::$project"].Content != want {
				t.Fatal("project skill lost source guidance or duplicated its global protocol")
			}
			if strings.Contains(provider.SystemPrompt(), protocol) || (tc.guidance != "" && strings.Contains(provider.SystemPrompt(), tc.guidance)) {
				t.Fatal("source-project instructions escaped their scoped skill")
			}
			caps := provider.Capabilities()
			if !slices.Contains(caps["weather::query"].SkillsList, "weather::$project") || caps["weather::admin.read"].Name != "" {
				t.Fatal("guidance selection changed delegated capability scope")
			}
		})
	}
}
