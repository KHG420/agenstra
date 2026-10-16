package agent

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestSourceScopeSupportsLargeCatalogWithoutChangingOwnership(t *testing.T) {
	for _, count := range []int{1, 100, 104, 550, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			names := make([]string, count)
			for i := range names {
				names[i] = fmt.Sprintf("records.operation.%04d", count-i)
			}
			before := slices.Clone(names)
			sources := []RunSource{{PackID: "records", Capabilities: names}}
			normalized, err := NormalizedSources("home", sources)
			if err != nil || len(normalized) != 1 || len(normalized[0].Capabilities) != count {
				t.Fatal("valid complete source scope rejected", count, err)
			}
			if !slices.IsSorted(normalized[0].Capabilities) || !slices.Equal(names, before) {
				t.Fatal("normalization changed caller scope or lost stable ordering")
			}
			normalized[0].Capabilities[0] = "changed"
			if !slices.Equal(names, before) {
				t.Fatal("normalized scope aliases caller data")
			}
		})
	}
}

func TestLargeSourceScopeKeepsOriginalValidationBoundaries(t *testing.T) {
	tooMany := make([]string, 1001)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("records.operation.%04d", i)
	}
	sources := []RunSource{}
	for i := 0; i < 9; i++ {
		sources = append(sources, RunSource{PackID: fmt.Sprintf("source%d", i), Capabilities: []string{"read"}})
	}
	for name, input := range map[string][]RunSource{
		"over 1000":        {{PackID: "records", Capabilities: tooMany}},
		"empty":            {{PackID: "records", Capabilities: []string{}}},
		"duplicate names":  {{PackID: "records", Capabilities: []string{"read", "read"}}},
		"duplicate packs":  {{PackID: "records", Capabilities: []string{"read"}}, {PackID: "records", Capabilities: []string{"write"}}},
		"origin repeated":  {{PackID: "home", Capabilities: []string{"read"}}},
		"qualified name":   {{PackID: "records", Capabilities: []string{"other::read"}}},
		"blank name":       {{PackID: "records", Capabilities: []string{" "}}},
		"overlong name":    {{PackID: "records", Capabilities: []string{strings.Repeat("x", 201)}}},
		"too many sources": sources,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizedSources("home", input); ErrorCode(err) != "source_scope_invalid" {
				t.Fatal("invalid source scope accepted or error changed", err)
			}
		})
	}
}
