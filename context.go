package agenstra

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

func contextCharacters(value any) int {
	raw, _ := CanonicalJSON(value)
	return utf8.RuneCount(raw)
}

// budgetContext builds a model projection without changing the complete run state.
// Tasks, followups, fact identities, active inspections, the latest skill and the
// latest outcome are required. Step rejects an oversized minimum before model IO.
func budgetContext(packet ContextPacket, state *RuntimeState, available int) ContextPacket {
	if contextCharacters(packet) <= available {
		return packet
	}
	baseOmissions := []string{}
	for _, note := range packet.ContextOmissions {
		if !strings.HasPrefix(note, "fact ") {
			baseOmissions = append(baseOmissions, note)
		}
	}
	// Array lengths describe the final preview, rather than an earlier projection.
	noteLimit := 12
	withNotes := func(candidate ContextPacket, omissions []string) ContextPacket {
		notes := arrayOmissions(state.Facts, candidate.Facts)
		notes = notes[:min(len(notes), noteLimit)]
		candidate.ContextOmissions = append(append([]string{}, omissions...), notes...)
		return candidate
	}
	loaded := packet.LoadedSkills
	order := []string{}
	for _, name := range state.LoadedSkills {
		if _, ok := loaded[name]; ok {
			order = append(order, name)
		}
	}
	olderSkills := []string{}
	packet.LoadedSkills = maps.Clone(loaded)
	for _, name := range order[:max(0, len(order)-1)] {
		notice := "skill: " + name + "; read_skill to load again"
		// A short skill can be smaller than its omission notice.
		if contextCharacters(map[string]string{name: loaded[name]}) > contextCharacters(notice) {
			olderSkills = append(olderSkills, name)
			delete(packet.LoadedSkills, name)
			baseOmissions = append(baseOmissions, notice)
		}
	}
	views := make([]FactView, len(state.Facts))
	for i, fact := range state.Facts {
		views[i] = factView(fact, 0)
		views[i].ReferenceAvailable = packet.Facts[i].ReferenceAvailable
	}
	recentSize := 0
	if len(views) > 0 {
		last := len(views) - 1
		recentSize = max(0, contextCharacters(packet.Facts[last])-contextCharacters(views[last]))
	}
	packet.Facts = views
	packet = withNotes(packet, baseOmissions)
	fitsRecent := func(candidate ContextPacket) bool {
		return contextCharacters(candidate)+recentSize <= available
	}
	// Repeated input payloads can be recovered from the audit trail. Keep outcomes.
	packet.Observations = append([]Observation{}, packet.Observations...)
	for i := range packet.Observations {
		if fitsRecent(packet) {
			break
		}
		if len(packet.Observations[i].Arguments) > 0 {
			packet.Observations[i].Arguments = JSON{}
			packet.Observations[i].ArgumentsOmitted = true
		}
	}
	// A catalog of individually small schemas can still exceed the total budget.
	packet.Capabilities = append([]JSON{}, packet.Capabilities...)
	indices := make([]int, len(packet.Capabilities))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(i, j int) bool {
		return contextCharacters(packet.Capabilities[indices[i]]["input_schema"]) > contextCharacters(packet.Capabilities[indices[j]]["input_schema"])
	})
	for _, i := range indices {
		if fitsRecent(packet) {
			break
		}
		view := packet.Capabilities[i]
		schema, ok := view["input_schema"].(map[string]any)
		if !ok {
			continue
		}
		deferred := maps.Clone(view)
		delete(deferred, "input_schema")
		fields := []string{}
		if properties, ok := schema["properties"].(map[string]any); ok {
			for name := range properties {
				fields = append(fields, name)
			}
			sort.Strings(fields)
			fields = fields[:min(32, len(fields))]
		}
		deferred["input_fields"] = fields
		deferred["schema_requires_inspection"] = true
		if contextCharacters(deferred) < contextCharacters(view) {
			packet.Capabilities[i] = deferred
		}
	}
	// Keep the last outcome, including failures with no Fact available to inspect.
	for len(packet.Observations) > 1 && !fitsRecent(packet) {
		packet.Observations = packet.Observations[1:]
		baseOmissions = slices.DeleteFunc(baseOmissions, func(note string) bool {
			return strings.HasPrefix(note, "observations:")
		})
		count := len(state.ModelObservations) - len(packet.Observations)
		baseOmissions = append([]string{fmt.Sprintf("observations: %d older entries", count)}, baseOmissions...)
		packet = withNotes(packet, baseOmissions)
	}
	// Detailed array-length hints are optional; omitted_paths remain on every Fact.
	for noteLimit > 0 && contextCharacters(packet) > available {
		noteLimit--
		packet = withNotes(packet, baseOmissions)
	}
	if contextCharacters(packet) > available {
		return packet
	}
	// Allocate remaining space to recent evidence first, counting provenance and
	// omission metadata as part of each complete candidate packet.
	for i := len(views) - 1; i >= 0; i-- {
		budget := min(6000, available-contextCharacters(packet)+2)
		for budget > 2 {
			preview := factView(state.Facts[i], budget)
			preview.ReferenceAvailable = views[i].ReferenceAvailable
			candidateViews := slices.Clone(views)
			candidateViews[i] = preview
			candidate := packet
			candidate.Facts = candidateViews
			candidate = withNotes(candidate, baseOmissions)
			if contextCharacters(candidate) <= available {
				views, packet = candidateViews, candidate
				break
			}
			budget /= 2
		}
	}
	// Restore older skills only after allocating evidence. Reading one again moves
	// it to the end of state.LoadedSkills, making it the required skill next round.
	for i := len(olderSkills) - 1; i >= 0; i-- {
		name := olderSkills[i]
		candidate := packet
		candidate.LoadedSkills = maps.Clone(packet.LoadedSkills)
		candidate.LoadedSkills[name] = loaded[name]
		notice := "skill: " + name + "; read_skill to load again"
		omissions := slices.DeleteFunc(slices.Clone(baseOmissions), func(note string) bool { return note == notice })
		candidate = withNotes(candidate, omissions)
		if contextCharacters(candidate) <= available {
			packet, baseOmissions = candidate, omissions
		}
	}
	return packet
}
