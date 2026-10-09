package agenstra

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type capabilityMatch struct {
	name  string
	score int
	exact bool
}

var qualifiedCapabilityQueryPattern = regexp.MustCompile(`[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)+`)

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
		// Keep full capability names while also discovering operations named as
		// module.operation, including identifiers directly beside Han text.
		for _, qualified := range qualifiedCapabilityQueryPattern.FindAllString(part, -1) {
			for _, component := range strings.Split(qualified, ".") {
				if !significantOnly || len(component) >= 3 {
					add(component)
				}
			}
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

func capabilitySearchText(cap CapabilityDescription) (string, string, string, []string) {
	fields := []string{}
	qualifiedOperations := []string{}
	addOperation := func(value string) {
		value = strings.ToLower(value)
		if qualifiedCapabilityQueryPattern.FindString(value) == value {
			qualifiedOperations = append(qualifiedOperations, value)
		}
	}
	// Search the complete input contract even when ModelView defers its schema.
	// Union discriminators and nested parameter guidance carry operation intent.
	var collect func(map[string]any)
	collect = func(schema map[string]any) {
		if description, ok := schema["description"].(string); ok {
			fields = append(fields, description)
		}
		if value, ok := schema["const"].(string); ok {
			fields = append(fields, value)
			addOperation(value)
		}
		if values, ok := schema["enum"].([]any); ok {
			for _, value := range values {
				if text, ok := value.(string); ok {
					fields = append(fields, text)
					addOperation(text)
				}
			}
		}
		if properties, ok := schema["properties"].(map[string]any); ok {
			for name, property := range properties {
				fields = append(fields, name)
				if child, ok := property.(map[string]any); ok {
					collect(child)
				}
			}
		}
		for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
			branches, _ := schema[keyword].([]any)
			for _, branch := range branches {
				if child, ok := branch.(map[string]any); ok {
					collect(child)
				}
			}
		}
		for _, keyword := range []string{"items", "additionalProperties"} {
			if child, ok := schema[keyword].(map[string]any); ok {
				collect(child)
			}
		}
	}
	// Normalize valid JSON containers, as ModelView does: SDK providers may
	// declare enum as []string or union branches as []JSON.
	raw, err := json.Marshal(cap.InputSchema)
	var schema map[string]any
	if err == nil {
		err = json.Unmarshal(raw, &schema)
	}
	if err == nil {
		collect(schema)
	}
	sort.Strings(fields)
	return strings.ToLower(cap.Name), strings.ToLower(cap.Description), strings.ToLower(strings.Join(fields, " ")), qualifiedOperations
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
		nameText, description, fields, qualifiedOperations := capabilitySearchText(cap)
		score := 0
		exact := false
		for _, term := range terms {
			exact = exact || nameText == term || containsString(qualifiedOperations, term)
			switch {
			case nameText == term:
				score += 8
			case strings.Contains(nameText, term):
				score += 5
			case strings.Contains(description, term):
				score += 2
			case strings.Contains(fields, term):
				// Keep operation and field discovery, without letting a broad
				// contract's incidental parameters outweigh its stated purpose.
				score++
			}
		}
		if score > 0 {
			matches = append(matches, capabilityMatch{name: name, score: score, exact: exact})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		// A fully qualified operation in the pinned contract identifies its
		// capability more precisely than shared module names or field words.
		if matches[i].exact != matches[j].exact {
			return matches[i].exact
		}
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
	if len(filteredSearch) > 0 {
		add(filteredSearch[0])
	}
	if state.InspectedCapability != nil {
		add(*state.InspectedCapability)
	}
	// A later subject search must not erase tools already discovered for an
	// unfinished multi-part request. Reuse the existing decision journal, while
	// reserving the first result for the newest search and rechecking all grants.
	for i := len(state.Decisions) - 1; i >= 0 && len(selected) < limit; i-- {
		decision := state.Decisions[i]
		kind, _ := decision["kind"].(string)
		switch kind {
		case "inspect_capability":
			name, _ := decision["name"].(string)
			add(name)
		case "search_capabilities":
			query, _ := decision["query"].(string)
			for _, name := range searchAuthorizedCapabilities(caps, grants, query, 1) {
				add(name)
			}
		}
	}
	for _, name := range filteredSearch {
		add(name)
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
