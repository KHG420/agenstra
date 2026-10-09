package agent

import (
	"slices"
	"sort"
	"strings"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

// RunSource is an explicit delegation for one other host project's pack.
// Capabilities are local names in that pack. The originating user's connection
// must also delegate them, and the target must verify that user's permissions.
type RunSource struct {
	PackID       string   `json:"pack_id"`
	Capabilities []string `json:"capabilities"`
}

// ProjectBinding retains the frozen project scope, target identity and release of a delegated source.
type ProjectBinding struct {
	RunSource
	Release string `json:"release"`
	Subject string `json:"subject"`
}

// NormalizedSources validates explicit source scopes and returns a stable sorted copy.
func NormalizedSources(origin string, sources []RunSource) ([]RunSource, error) {
	if len(sources) > 8 {
		return nil, NewHostError("source_scope_invalid")
	}
	out := []RunSource{}
	seen := map[string]bool{}
	for _, source := range sources {
		if source.PackID == origin || !RegistryID.MatchString(source.PackID) || seen[source.PackID] || len(source.Capabilities) < 1 || len(source.Capabilities) > 100 {
			return nil, NewHostError("source_scope_invalid")
		}
		seen[source.PackID] = true
		names := slices.Clone(source.Capabilities)
		sort.Strings(names)
		for i, name := range names {
			if strings.TrimSpace(name) == "" || len(name) > 200 || strings.Contains(name, "::") || i > 0 && name == names[i-1] {
				return nil, NewHostError("source_scope_invalid")
			}
		}
		out = append(out, RunSource{PackID: source.PackID, Capabilities: names})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PackID < out[j].PackID })
	return out, nil
}

// RunBindings decodes the run's frozen project bindings and rejects malformed state.
func RunBindings(run StoredRun) ([]ProjectBinding, error) {
	out := []ProjectBinding{}
	if value, ok := run.State["project_sources"]; ok {
		raw, err := CanonicalJSON(value)
		if err != nil || jsonvalue.DecodeStrict(raw, &out) != nil {
			return nil, NewHostError("run_state_invalid")
		}
	}
	return out, nil
}

// SourceScopeEqual checks whether a request selects the same source scope as the saved run.
func SourceScopeEqual(run StoredRun, sources []RunSource) bool {
	bindings, err := RunBindings(run)
	if err != nil {
		return false
	}
	previous := []RunSource{}
	for _, b := range bindings {
		previous = append(previous, b.RunSource)
	}
	return EqualSources(previous, sources)
}

// EqualSources compares normalized source scopes by their canonical JSON values.
func EqualSources(a, b []RunSource) bool {
	x, err := CanonicalJSON(a)
	if err != nil {
		return false
	}
	y, err := CanonicalJSON(b)
	if err != nil {
		return false
	}
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return string(x) == string(y)
}
