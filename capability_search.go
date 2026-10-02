package agenstra

import (
	"sort"
	"strings"
	"unicode"
)

type capabilityMatch struct {
	name  string
	score int
}

// Han text has no space-delimited words. Overlapping short phrases retain
// natural-language matches without a dictionary, model request or business IO.
func capabilityQueryTerms(query string, significantOnly bool) []string {
	terms, seen := []string{}, map[string]bool{}
	add := func(term string) {
		if term != "" && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	for _, part := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '.' }) {
		runes := []rune(part)
		hasHan := false
		for _, r := range runes {
			if unicode.Is(unicode.Han, r) {
				hasHan = true
				break
			}
		}
		if !significantOnly || hasHan || len(runes) >= 3 {
			add(part)
		}
		if hasHan {
			for i := range runes {
				for _, n := range []int{2, 3} {
					if i+n > len(runes) {
						continue
					}
					allHan := true
					for _, r := range runes[i : i+n] {
						if !unicode.Is(unicode.Han, r) {
							allHan = false
							break
						}
					}
					if allHan {
						add(string(runes[i : i+n]))
					}
				}
			}
		}
	}
	return terms
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
	terms := capabilityQueryTerms(query, false)
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
	addSingle := func(name string) {
		cap, ok := caps[name]
		if !ok || len(selected) >= limit || seen[name] || !grants[cap.Name] {
			return
		}
		selected = append(selected, name)
		seen[name] = true
	}
	add := func(name string) {
		cap, ok := caps[name]
		if !ok || seen[name] || !grants[cap.Name] {
			return
		}
		// Reserve a slot for the authorized prerequisite before revealing a page
		// action. A limit of one may show context first; search remains available.
		if strings.HasPrefix(cap.Name, "ui.") && cap.Operation != nil && cap.Operation.PollCapability == "ui.command_status" {
			addSingle("ui.get_context")
		}
		addSingle(name)
	}
	for _, name := range filteredSearch {
		add(name)
	}
	if state.InspectedCapability != nil {
		add(*state.InspectedCapability)
	}
	significant := capabilityQueryTerms(instruction, true)
	for _, name := range searchAuthorizedCapabilities(caps, grants, strings.Join(significant, " "), limit) {
		add(name)
	}
	for _, name := range authorized {
		add(name)
	}
	return selected, len(authorized), filteredSearch
}
