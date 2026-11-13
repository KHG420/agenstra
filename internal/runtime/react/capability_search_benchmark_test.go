package react

import (
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func BenchmarkCapabilitySearch(b *testing.B) {
	caps := make(map[string]agentcontract.CapabilityDescription, 300)
	grants := make(map[string]bool, 300)
	for i := 0; i < 300; i++ {
		name := fmt.Sprintf("mcp__shipping__voyage_query_%03d", i)
		caps[name] = agentcontract.CapabilityDescription{
			Name: name, Description: "Query authorized shipping voyages, port names and current voyage status.",
			InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
				"query": agentcontract.JSON{"type": "string", "description": strings.Repeat("Original observed vessel or voyage selector. ", 12)},
				"page":  agentcontract.JSON{"type": "integer", "minimum": 1},
			}},
		}
		grants[name] = true
	}
	for _, query := range []string{"mcp__shipping__voyage_query_299", "shipping voyage port"} {
		b.Run(query, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if len(searchAuthorizedCapabilities(caps, grants, query, 4)) == 0 {
					b.Fatal("authorized search returned no result")
				}
			}
		})
	}
	state := &agentcontract.RuntimeState{}
	for i := 0; i < 16; i++ {
		state.Decisions = append(state.Decisions, agentcontract.JSON{"kind": "search_capabilities", "query": "mcp__shipping__voyage_query_299"})
	}
	b.Run("retained_searches", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if selected, _, _ := selectedCapabilityNames(caps, grants, "Read a voyage", state, 4); len(selected) != 4 {
				b.Fatal("retained discovery lost its bounded catalog")
			}
		}
	})
}
