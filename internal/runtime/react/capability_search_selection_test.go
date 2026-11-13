package react

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCapabilitySearchSelectsAuthorizedNamesInRequestedOrder(t *testing.T) {
	caps := searchTestCapabilities()
	grants := map[string]bool{"alpha.read": true, "beta.read": true, "omega.submit_invoice": true}
	for _, tc := range []struct {
		query string
		limit int
		want  []string
	}{
		{"select:omega.submit_invoice,beta.read,omega.submit_invoice,secret.audit,missing", 4, []string{"omega.submit_invoice", "beta.read"}},
		{" SELECT: BETA.READ , alpha.read ", 4, []string{"beta.read", "alpha.read"}},
		{"select:omega.submit_invoice,beta.read", 1, []string{"omega.submit_invoice"}},
		{"select:secret.audit,missing", 4, nil},
		{"select:", 4, nil},
		{"select:alpha.read", 0, nil},
		{" OMEGA.SUBMIT_INVOICE ", 4, []string{"omega.submit_invoice"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			if got := searchAuthorizedCapabilities(caps, grants, tc.query, tc.limit); !slices.Equal(got, tc.want) {
				t.Fatalf("selected tools differ: got %v, want %v", got, tc.want)
			}
		})
	}
	grants["beta.read"] = false
	if got := searchAuthorizedCapabilities(caps, grants, "select:beta.read,alpha.read", 4); !slices.Equal(got, []string{"alpha.read"}) {
		t.Fatal("selection retained a revoked tool", got)
	}
}

func TestCapabilitySearchRequiredTermsAndWordBoundaries(t *testing.T) {
	caps := map[string]agentcontract.CapabilityDescription{
		"mcp__ledger__getRecord": {Name: "mcp__ledger__getRecord", Description: "Read a record from the ledger"},
		"mcp__ledger__sendMail":  {Name: "mcp__ledger__sendMail", Description: "Send a ledger report"},
		"mcp__teams__sendMail":   {Name: "mcp__teams__sendMail", Description: "Send a team report"},
		"mcp__private__sendMail": {Name: "mcp__private__sendMail", Description: "Send a ledger report"},
		"data.lookup":            {Name: "data.lookup", Description: "Find the port for a vessel"},
		"data.summary":           {Name: "data.summary", Description: "Read a report for a vessel"},
		"data.fields": {Name: "data.fields", InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
			"departurePort": agentcontract.JSON{"type": "string"},
		}}},
		"local.compute": {Name: "local.compute", Description: "计算起讫港之间的距离与方位"},
	}
	grants := map[string]bool{}
	for name := range caps {
		grants[name] = name != "mcp__private__sendMail"
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"+ledger +send report", []string{"mcp__ledger__sendMail"}},
		{"+ledger +missing send", nil},
		{"+距离 方位", []string{"local.compute"}},
		{"port", []string{"data.lookup", "data.fields"}},
		{"departure", []string{"data.fields"}},
		{"get record", []string{"mcp__ledger__getRecord"}},
		{"mcp__ledger__", []string{"mcp__ledger__getRecord", "mcp__ledger__sendMail"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got := searchAuthorizedCapabilities(caps, grants, tc.query, 8)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("query %q returned %v, want %v", tc.query, got, tc.want)
			}
		})
	}
	cap := caps["data.lookup"]
	cap.Description = "Find an anchorage"
	caps[cap.Name] = cap
	if got := searchAuthorizedCapabilities(caps, grants, "port", 8); slices.Contains(got, cap.Name) {
		t.Fatal("a new search reused stale same-name metadata", got)
	}
	for _, query := range []string{"+\xff port", "[.*", "\x00", ""} {
		searchAuthorizedCapabilities(caps, grants, query, 4)
	}
}

func TestCapabilitySearchRestoresMultipleSelectedContracts(t *testing.T) {
	caps := searchTestCapabilities()
	for _, name := range []string{"delta.read", "lambda.read"} {
		caps[name] = agentcontract.CapabilityDescription{Name: name, InputSchema: agentcontract.JSON{"type": "object"}}
	}
	grants := map[string]bool{"alpha.read": true, "beta.read": true, "omega.submit_invoice": true, "delta.read": true, "lambda.read": true}
	provider := &coreTestProvider{caps: caps}
	queries := []string{"select:omega.submit_invoice,beta.read", "delta.read"}
	runtime := &AgentRuntime{Provider: provider, Grants: grants, MaxContextCapabilities: 3, Model: decisionModelFunc(func(context.Context, agentcontract.ContextPacket, string) (agentcontract.Decision, error) {
		query := queries[0]
		queries = queries[1:]
		return agentcontract.Decision{Kind: "search_capabilities", Query: query}, nil
	})}
	state, err := runtime.NewState("Help me", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	packet := runtime.Context(state)
	if names := capabilityNames(packet); len(names) != 3 || !slices.Equal(names[:2], []string{"omega.submit_invoice", "beta.read"}) {
		t.Fatal("explicit selection lost its requested priority", names)
	}
	if len(state.Progress.Inspections) != 2 {
		t.Fatal("selected contracts were not recorded as discovery progress", state.Progress.Inspections)
	}
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored agentcontract.RuntimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	packet = runtime.Context(&restored)
	if names := capabilityNames(packet); !slices.Equal(names, []string{"delta.read", "omega.submit_invoice", "beta.read"}) {
		t.Fatal("recovery lost previously selected tools", names)
	}
	for _, cap := range packet.Capabilities {
		if cap["input_schema"] == nil || cap["schema_requires_inspection"] == true {
			t.Fatal("restored selection did not expose the callable contract", cap)
		}
	}
	grants["beta.read"] = false
	if names := capabilityNames(runtime.Context(&restored)); slices.Contains(names, "beta.read") || slices.Contains(names, "secret.audit") || len(names) > 3 || provider.called != 0 {
		t.Fatal("discovery bypassed authorization, capacity or provider isolation", names, provider.called)
	}
}

func TestCapabilitySearchPreservesSingularQueriesForPluralDescriptions(t *testing.T) {
	caps := map[string]agentcontract.CapabilityDescription{
		"account.discover": {Name: "account.discover", Description: "Discover vessels, voyages, details, ports and statuses"},
		"data.summary":     {Name: "data.summary", Description: "Read a report and statusbar"},
	}
	grants := map[string]bool{"account.discover": true, "data.summary": true}
	for _, query := range []string{"vessel detail voyage", "port", "status"} {
		if got := searchAuthorizedCapabilities(caps, grants, query, 4); !slices.Equal(got, []string{"account.discover"}) {
			t.Fatalf("singular query %q lost whole plural words or matched an unrelated substring: %v", query, got)
		}
	}
}
