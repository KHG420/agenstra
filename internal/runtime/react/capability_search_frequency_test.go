package react

import (
	"fmt"
	"slices"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCapabilitySearchCommonBoilerplateDoesNotDisplaceDistinctSubject(t *testing.T) {
	caps := map[string]agentcontract.CapabilityDescription{
		"files.list": {Name: "files.list", Description: "List workspace documents.", InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"postal_zone": agentcontract.JSON{"type": "string"}}}},
	}
	grants := map[string]bool{"files.list": true}
	for i := range 20 {
		name := fmt.Sprintf("read.shared_%d", i)
		caps[name] = agentcontract.CapabilityDescription{Name: name, Description: "Read shared state. Return JSON."}
		grants[name] = true
	}
	query := "Read workspace documents; return JSON."
	result := searchAuthorizedCapabilities(caps, grants, query, 1)
	if !slices.Equal(result, []string{"files.list"}) {
		t.Fatal("common output and read words displaced the requested subject", result)
	}
	// Restricted catalogs must not affect scoring, or disclose their existence
	// through the ordering of a user's permitted tools.
	before := searchAuthorizedCapabilities(caps, grants, query, 20)
	for i := range 100 {
		name := fmt.Sprintf("private.workspace_documents_%d", i)
		caps[name] = agentcontract.CapabilityDescription{Name: name, Description: "List workspace documents."}
	}
	after := searchAuthorizedCapabilities(caps, grants, query, 20)
	if !slices.Equal(before, after) {
		t.Fatal("ungranted capabilities changed authorized search ordering", before, after)
	}
	if got := searchAuthorizedCapabilities(caps, grants, "postal_zone", 1); !slices.Equal(got, []string{"files.list"}) {
		t.Fatal("frequency weighting lost input-field discovery", got)
	}
}

func TestCapabilitySearchExactIdentityKeepsPriorityOverRareProse(t *testing.T) {
	caps := map[string]agentcontract.CapabilityDescription{
		"files.list": {Name: "files.list", Description: "Generic read operation."},
		"other.list": {Name: "other.list", Description: "Rarely used workspace documents.", InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"operation": agentcontract.JSON{"const": "files.list"}}}},
	}
	grants := map[string]bool{"files.list": true, "other.list": true}
	if result := searchAuthorizedCapabilities(caps, grants, "files.list workspace documents", 1); !slices.Equal(result, []string{"files.list"}) {
		t.Fatal("an explicit identity lost priority to incidental prose or a schema choice", result)
	}
}
