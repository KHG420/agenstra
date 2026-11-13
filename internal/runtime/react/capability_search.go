package react

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type capabilityMatch struct {
	name      string
	score     float64
	exact     bool
	exactName bool
}

type capabilitySearchDocument struct {
	name, nameText, description, fields string
	parts, qualifiedOperations          []string
}

type capabilitySearchTerm struct {
	text         string
	wordBoundary bool
}

var qualifiedCapabilityQueryPattern = regexp.MustCompile(`[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)+`)
var projectCapabilityQueryPattern = regexp.MustCompile(`[a-z][a-z0-9_.-]*::[a-z_][a-z0-9_-]*(?:\.[a-z_][a-z0-9_-]*)*`)
var capabilityCamelCasePattern = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func capabilityNameParts(name string) []string {
	separated := name
	if strings.IndexFunc(name, unicode.IsUpper) >= 0 {
		separated = capabilityCamelCasePattern.ReplaceAllString(name, "$1 $2")
	}
	return strings.FieldsFunc(strings.ToLower(separated), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func compileCapabilitySearchTerm(term string) capabilitySearchTerm {
	return capabilitySearchTerm{text: term, wordBoundary: strings.IndexFunc(term, func(r rune) bool { return unicode.Is(unicode.Han, r) }) < 0}
}

func (term capabilitySearchTerm) matches(text string) bool {
	if !term.wordBoundary {
		return strings.Contains(text, term.text)
	}
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], term.text)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(term.text)
		before, _ := utf8.DecodeLastRuneInString(text[:index])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if !capabilityWordRune(before) && !capabilityWordRune(after) {
			return true
		}
		offset = index + 1
	}
	return false
}

func capabilityWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func (doc capabilitySearchDocument) weight(term capabilitySearchTerm) float64 {
	switch {
	case doc.nameText == term.text || slices.Contains(doc.parts, term.text):
		return 8
	case strings.Contains(doc.nameText, term.text):
		return 5
	case term.matches(doc.description):
		return 2
	case term.matches(doc.fields):
		return 1
	default:
		return 0
	}
}

func capabilitySelectionQuery(query string) ([]string, bool) {
	prefix, names, found := strings.Cut(strings.TrimSpace(query), ":")
	if !found || !strings.EqualFold(prefix, "select") {
		return nil, false
	}
	return strings.Split(names, ","), true
}

func exactAuthorizedCapability(caps map[string]agentcontract.CapabilityDescription, grants map[string]bool, requested string) string {
	if cap, exists := caps[requested]; exists && grants[cap.Name] {
		return requested
	}
	matches := []string{}
	for name, cap := range caps {
		if grants[cap.Name] && (strings.EqualFold(name, requested) || strings.EqualFold(cap.Name, requested)) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func directCapabilitySearch(caps map[string]agentcontract.CapabilityDescription, grants map[string]bool, query string, limit int) ([]string, bool) {
	query = strings.TrimSpace(query)
	if requested, selected := capabilitySelectionQuery(query); selected {
		matches := []string{}
		for _, name := range requested {
			if len(matches) >= limit {
				break
			}
			match := exactAuthorizedCapability(caps, grants, strings.TrimSpace(name))
			if match != "" && !slices.Contains(matches, match) {
				matches = append(matches, match)
			}
		}
		return matches, true
	}
	if match := exactAuthorizedCapability(caps, grants, query); match != "" {
		return []string{match}, true
	}
	if strings.HasPrefix(strings.ToLower(query), "mcp__") {
		matches := []string{}
		for name, cap := range caps {
			if grants[cap.Name] && strings.HasPrefix(strings.ToLower(cap.Name), strings.ToLower(query)) {
				matches = append(matches, name)
			}
		}
		if len(matches) > 0 {
			sort.Strings(matches)
			return matches[:min(limit, len(matches))], true
		}
	}
	return nil, false
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
	// Preserve the complete routed identity before word splitting separates the
	// project delimiter or hyphens. A local operation alone cannot select a source.
	for _, name := range projectCapabilityQueryPattern.FindAllString(strings.ToLower(query), -1) {
		add(name)
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
			// Han prose can touch an ASCII capability identity without a space.
			// Retain that identity so exact-name ranking can still recognize it.
			for _, adjacent := range strings.FieldsFunc(part, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
				if strings.Trim(adjacent, "._") != "" && (!significantOnly || len([]rune(adjacent)) >= 3) {
					add(adjacent)
				}
			}
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

func capabilitySearchText(cap agentcontract.CapabilityDescription) (string, string, string, []string) {
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
				fields = append(fields, capabilityNameParts(name)...)
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
func searchAuthorizedCapabilities(caps map[string]agentcontract.CapabilityDescription, grants map[string]bool, query string, limit int) []string {
	return capabilitySearcher(caps, grants)(query, limit)
}

// capabilitySearcher reuses prepared metadata during one fixed-catalog selection
// pass. Each new pass receives current contracts and grants from its caller.
func capabilitySearcher(caps map[string]agentcontract.CapabilityDescription, grants map[string]bool) func(string, int) []string {
	var documents []capabilitySearchDocument
	return func(query string, limit int) []string {
		if limit < 1 {
			return []string{}
		}
		if matches, direct := directCapabilitySearch(caps, grants, query, limit); direct {
			return matches
		}
		optional, required := []string{}, []capabilitySearchTerm{}
		for _, word := range strings.Fields(strings.ToLower(query)) {
			if strings.HasPrefix(word, "+") && len(word) > 1 {
				word = strings.TrimPrefix(word, "+")
				required = append(required, compileCapabilitySearchTerm(word))
			}
			optional = append(optional, word)
		}
		terms := []capabilitySearchTerm{}
		for _, term := range capabilityQueryTerms(strings.Join(optional, " "), false) {
			terms = append(terms, compileCapabilitySearchTerm(term))
		}
		if len(terms) == 0 {
			return []string{}
		}
		if documents == nil {
			documents = make([]capabilitySearchDocument, 0, len(caps))
			for name, cap := range caps {
				if !grants[cap.Name] {
					continue
				}
				nameText, description, fields, operations := capabilitySearchText(cap)
				documents = append(documents, capabilitySearchDocument{name: name, nameText: nameText, description: description, fields: fields, parts: capabilityNameParts(cap.Name), qualifiedOperations: operations})
			}
		}
		candidates := documents
		if len(required) > 0 {
			candidates = make([]capabilitySearchDocument, 0, len(documents))
			for _, doc := range documents {
				if slices.ContainsFunc(required, func(term capabilitySearchTerm) bool { return doc.weight(term) == 0 }) {
					continue
				}
				candidates = append(candidates, doc)
			}
		}
		return rankCapabilitySearch(candidates, terms, limit)
	}
}

func rankCapabilitySearch(documents []capabilitySearchDocument, terms []capabilitySearchTerm, limit int) []string {
	weights := map[string]float64{}
	for _, term := range terms {
		frequency := 0
		for _, doc := range documents {
			if doc.weight(term) > 0 {
				frequency++
			}
		}
		// Keep the existing lexical contribution and add BM25's positive inverse
		// document frequency. Retaining the base avoids letting rare incidental
		// fields displace an operation named by otherwise common subject words.
		// Count only the live authorized catalog; restricted tools cannot alter it.
		weights[term.text] = 1 + math.Log1p((float64(len(documents)-frequency)+0.5)/(float64(frequency)+0.5))
	}
	matches := []capabilityMatch{}
	for _, doc := range documents {
		score := 0.0
		exact := false
		exactName := false
		for _, term := range terms {
			exactName = exactName || doc.nameText == term.text
			exact = exact || doc.nameText == term.text || agentcontract.ContainsString(doc.qualifiedOperations, term.text)
			score += doc.weight(term) * weights[term.text]
		}
		if score > 0 {
			matches = append(matches, capabilityMatch{name: doc.name, score: score, exact: exact, exactName: exactName})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		// An explicit capability identity outranks an operation mentioned inside
		// another capability's contract, even when both describe the same action.
		if matches[i].exactName != matches[j].exactName {
			return matches[i].exactName
		}

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

// Rephrasing a query without finding new candidates is not discovery progress.
// Use the existing decision journal so recovery preserves this bound as well.
func (r *AgentRuntime) capabilitySearchStalled(state *agentcontract.RuntimeState) bool {
	if r.MaxContextCapabilities <= 0 || state.Progress == nil || state.Progress.NoProgressRounds < 2 {
		return false
	}
	var candidates []string
	searches := 0
	search := capabilitySearcher(r.Provider.Capabilities(), r.Grants)
	start := max(0, len(state.Decisions)-min(8, state.Progress.NoProgressRounds+1))
	for i := len(state.Decisions) - 1; i >= start; i-- {
		decision := state.Decisions[i]
		switch decision["kind"] {
		case "tool_batch", "request_input", "final":
			return false
		case "search_capabilities":
			query, _ := decision["query"].(string)
			matches := search(query, r.MaxContextCapabilities)
			slices.Sort(matches)
			if searches > 0 && !slices.Equal(matches, candidates) {
				return false
			}
			candidates = matches
			searches++
			if searches == 2 {
				return true
			}
		}
	}
	return false
}

func selectedCapabilityNames(caps map[string]agentcontract.CapabilityDescription, grants map[string]bool, instruction string, state *agentcontract.RuntimeState, limit int) ([]string, int, []string) {
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
	search := capabilitySearcher(caps, grants)
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
	_, explicitSelection := capabilitySelectionQuery(state.CapabilitySearchQuery)
	for i, name := range filteredSearch {
		if i > 0 && !explicitSelection {
			break
		}
		add(name)
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
			resultLimit := 1
			if _, selected := capabilitySelectionQuery(query); selected {
				resultLimit = limit
			}
			for _, name := range search(query, resultLimit) {
				add(name)
			}
		}
	}
	for _, name := range filteredSearch {
		add(name)
	}
	if len(selected) < limit {
		significant := capabilityQueryTerms(instruction, true)
		for _, name := range search(strings.Join(significant, " "), limit) {
			add(name)
		}
	}
	for _, name := range authorized {
		add(name)
	}
	return selected, len(authorized), filteredSearch
}
