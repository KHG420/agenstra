package agenstra

import (
	"context"
	"strings"
	"testing"
)

func searchTestCapabilities() map[string]CapabilityDescription {
	return map[string]CapabilityDescription{
		"alpha.read":           {Name: "alpha.read", Description: "Read accounts", InputSchema: JSON{"type": "object", "properties": JSON{"account_id": JSON{"type": "string"}}}},
		"beta.read":            {Name: "beta.read", Description: "Read accounts", InputSchema: JSON{"type": "object"}},
		"omega.submit_invoice": {Name: "omega.submit_invoice", Description: "Submit invoice", InputSchema: JSON{"type": "object", "properties": JSON{"invoice_id": JSON{"type": "string"}}}},
		"secret.audit":         {Name: "secret.audit", Description: "Audit invoices", InputSchema: JSON{"type": "object"}},
	}
}

func capabilityNames(packet ContextPacket) []string {
	names := []string{}
	for _, cap := range packet.Capabilities {
		name, _ := cap["name"].(string)
		names = append(names, name)
	}
	return names
}

func TestCapabilitySearchFindsOmittedAuthorizedCapability(t *testing.T) {
	caps := searchTestCapabilities()
	provider := &coreTestProvider{caps: caps}
	grants := map[string]bool{"alpha.read": true, "beta.read": true, "omega.submit_invoice": true}
	r := &AgentRuntime{Provider: provider, Grants: grants, MaxContextCapabilities: 2, Model: decisionModelFunc(func(context.Context, ContextPacket, string) (Decision, error) {
		return Decision{Kind: "search_capabilities", Query: "submit_invoice"}, nil
	})}
	state, err := r.NewState("Help me", "")
	if err != nil {
		t.Fatal(err)
	}
	initial := r.Context(state)
	if len(initial.Capabilities) != 2 || initial.CapabilityCatalogTotal != 3 || initial.RuntimeFeatures[len(initial.RuntimeFeatures)-1] != "capability_search" || strings.Contains(strings.Join(capabilityNames(initial), ","), "omega.submit_invoice") {
		t.Fatal("bounded initial catalog", capabilityNames(initial), initial.CapabilityCatalogTotal)
	}
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if provider.called != 0 {
		t.Fatal("catalog search invoked a business capability")
	}
	packet := r.Context(state)
	if len(packet.Capabilities) != 2 || capabilityNames(packet)[0] != "omega.submit_invoice" || len(packet.CapabilitySearchResults) != 1 || packet.CapabilitySearchResults[0] != "omega.submit_invoice" {
		t.Fatal("search did not reveal omitted capability", capabilityNames(packet), packet.CapabilitySearchResults)
	}
	grants["omega.submit_invoice"] = false
	packet = r.Context(state)
	if strings.Contains(strings.Join(capabilityNames(packet), ","), "omega.submit_invoice") || len(packet.CapabilitySearchResults) != 0 {
		t.Fatal("revoked capability remained visible", capabilityNames(packet), packet.CapabilitySearchResults)
	}
}

func TestCapabilitySearchEmptyStableAndLegacy(t *testing.T) {
	if _, err := strictDecision([]byte(`{"kind":"search_capabilities","query":"invoice"}`)); err != nil {
		t.Fatal(err)
	}
	for _, decision := range []Decision{{Kind: "search_capabilities", Query: " "}, {Kind: "search_capabilities", Query: strings.Repeat("x", 301)}} {
		if decision.Validate() == nil {
			t.Fatal("accepted invalid search query", decision.Query)
		}
	}
	caps := searchTestCapabilities()
	grants := map[string]bool{"alpha.read": true, "beta.read": true, "omega.submit_invoice": true}
	for i := 0; i < 10; i++ {
		got := searchAuthorizedCapabilities(caps, grants, "read accounts", 2)
		if len(got) != 2 || got[0] != "alpha.read" || got[1] != "beta.read" {
			t.Fatal("search order changed", got)
		}
	}
	if got := searchAuthorizedCapabilities(caps, grants, "unmatched_term", 2); len(got) != 0 {
		t.Fatal("unexpected match", got)
	}
	searchRuntime := &AgentRuntime{Provider: &coreTestProvider{caps: caps}, Grants: grants, MaxContextCapabilities: 1, Model: decisionModelFunc(func(context.Context, ContextPacket, string) (Decision, error) {
		return Decision{Kind: "search_capabilities", Query: "unmatched_term"}, nil
	})}
	searchState, _ := searchRuntime.NewState("Help me", "")
	if err := searchRuntime.Step(t.Context(), searchState, nil); err != nil {
		t.Fatal(err)
	}
	if packet := searchRuntime.Context(searchState); len(packet.CapabilitySearchResults) != 0 || !strings.Contains(strings.Join(packet.ContextOmissions, " "), "no authorized matches") {
		t.Fatal("empty search was not visible to model", packet.ContextOmissions)
	}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: caps}, Grants: grants}
	state, _ := r.NewState("Help me", "")
	packet := r.Context(state)
	if len(packet.Capabilities) != 3 || packet.CapabilityCatalogTotal != 0 {
		t.Fatal("default catalog changed", capabilityNames(packet), packet.CapabilityCatalogTotal)
	}
	settings := DefaultHostSettings()
	for _, invalid := range []int{-1, 201} {
		settings.MaxContextCapabilities = invalid
		if settings.Validate() == nil {
			t.Fatal("accepted invalid capability limit", invalid)
		}
	}
	settings.MaxContextCapabilities = 200
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
}
