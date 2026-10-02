package agenstra

import (
	"sort"
	"strings"
)

type capabilityMatch struct {
	name  string
	score int
}

func capabilitySearchText(cap CapabilityDescription) (string, string, string) {
	fields := []string{}
	if properties, ok := cap.InputSchema["properties"].(map[string]any); ok {
		for field := range properties {
			fields = append(fields, field)
		}
	}
	return strings.ToLower(cap.Name), strings.ToLower(cap.Description), strings.ToLower(strings.Join(fields, " "))
}

// The search reads only the pinned capability catalog and live grants. It
// performs no provider invocation or business IO.
func searchAuthorizedCapabilities(caps map[string]CapabilityDescription, grants map[string]bool, query string, limit int) []string {
	if limit < 1 {
		return []string{}
	}
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return []string{}
	}
	matches := []capabilityMatch{}
	for name, cap := range caps {
		if !grants[cap.Name] {
			continue
		}
		nameText, description, fields := capabilitySearchText(cap)
		score := 0
		for _, term := range terms {
			switch {
			case nameText == term:
				score += 8
			case strings.Contains(nameText, term):
				score += 5
			case strings.Contains(fields, term):
				score += 3
			case strings.Contains(description, term):
				score++
			}
		}
		if score > 0 {
			matches = append(matches, capabilityMatch{name: name, score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].name < matches[j].name
	})
	result := make([]string, 0, min(limit, len(matches)))
	for _, match := range matches[:min(limit, len(matches))] {
		result = append(result, match.name)
	}
	return result
}

func selectedCapabilityNames(caps map[string]CapabilityDescription, grants map[string]bool, instruction string, state *RuntimeState, limit int) ([]string, int, []string) {
	authorized := []string{}
	for name, cap := range caps {
		if grants[cap.Name] {
			authorized = append(authorized, name)
		}
	}
	sort.Strings(authorized)
	filteredSearch := []string{}
	for _, name := range state.CapabilitySearchResults {
		if cap, ok := caps[name]; ok && grants[cap.Name] {
			filteredSearch = append(filteredSearch, name)
		}
	}
	if limit <= 0 || len(authorized) <= limit {
		return authorized, len(authorized), filteredSearch
	}
	selected := []string{}
	seen := map[string]bool{}
	add := func(name string) {
		cap, ok := caps[name]
		if !ok || len(selected) >= limit || seen[name] || !grants[cap.Name] {
			return
		}
		selected = append(selected, name)
		seen[name] = true
	}
	for _, name := range filteredSearch {
		add(name)
	}
	if state.InspectedCapability != nil {
		add(*state.InspectedCapability)
	}
	significant := []string{}
	for _, term := range strings.Fields(instruction) {
		if len([]rune(term)) >= 3 {
			significant = append(significant, term)
		}
	}
	for _, name := range searchAuthorizedCapabilities(caps, grants, strings.Join(significant, " "), limit) {
		add(name)
	}
	for _, name := range authorized {
		add(name)
	}
	return selected, len(authorized), filteredSearch
}
