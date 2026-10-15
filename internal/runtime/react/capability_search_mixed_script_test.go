package react

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCapabilitySearchRecognizesIdentifierBesideHanText(t *testing.T) {
	const target = "PeopleService_GetPerson"
	caps := map[string]agentcontract.CapabilityDescription{
		target:                {Name: target, Description: "Get a person"},
		"ShareService_Create": {Name: "ShareService_Create", Description: "Create a person share"},
	}
	grants := map[string]bool{target: true, "ShareService_Create": true}
	for _, query := range []string{
		"读取PeopleService_GetPerson资料",
		"PeopleService_GetPerson读取资料",
		"请读取PeopleService_GetPerson",
		"请读取PEOPLESERVICE_GETPERSON资料",
	} {
		t.Run(query, func(t *testing.T) {
			got := searchAuthorizedCapabilities(caps, grants, query, 1)
			if len(got) != 1 || got[0] != target {
				t.Fatalf("inline capability identity was lost: %v", got)
			}
		})
	}
	grants[target] = false
	if got := searchAuthorizedCapabilities(caps, grants, "读取PeopleService_GetPerson资料", 1); agentcontract.ContainsString(got, target) {
		t.Fatal("inline identity exposed a revoked capability", got)
	}
}

func TestBoundedCatalogKeepsInlineRequestedIdentities(t *testing.T) {
	caps := map[string]agentcontract.CapabilityDescription{
		"AService_CreateShare":    {Name: "AService_CreateShare", Description: "Create a record share"},
		"PeopleService_GetPerson": {Name: "PeopleService_GetPerson", Description: "Get a person"},
		"PeopleService_GetStats":  {Name: "PeopleService_GetStats", Description: "Get person statistics"},
		"PrivateService_GetStats": {Name: "PrivateService_GetStats", Description: "Get private statistics"},
	}
	grants := map[string]bool{
		"AService_CreateShare": true, "PeopleService_GetPerson": true, "PeopleService_GetStats": true,
	}
	names, total, _ := selectedCapabilityNames(caps, grants, "请分别读取PeopleService_GetPerson和PeopleService_GetStats，不要调用PrivateService_GetStats。", &agentcontract.RuntimeState{}, 2)
	if total != 3 || len(names) != 2 || !agentcontract.ContainsString(names, "PeopleService_GetPerson") || !agentcontract.ContainsString(names, "PeopleService_GetStats") {
		t.Fatal("bounded catalog displaced the requested identities", names, total)
	}

	// Punctuation between Han phrases must not become a match-all dot/underscore query.
	for _, term := range capabilityQueryTerms("请查看._.字段", false) {
		if term == "._." || term == "." || term == "_" {
			t.Fatal("punctuation became a capability query", term)
		}
	}
}
