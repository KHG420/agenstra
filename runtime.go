package agenstra

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// AgentRuntime owns decision budgets, references and a run's mutable execution state.
// A runtime is used by one driver at a time; its provider and model remain caller-owned.
type AgentRuntime struct {
	ContextPolicy              ContextPolicy
	ModelContextWindowTokens   int64
	MaxModelInputTokens        int64
	ModelOutputReserveTokens   int
	ModelProtocolReserveTokens int64
	OriginPackID               string
	Provider                   CapabilityProvider
	Model                      DecisionModel
	Grants                     map[string]bool
	ConnectionID               string
	Durable                    bool
	MaxModelRounds             int
	MaxToolCalls               int
	MaxRepeatedCall            int
	MaxContextCharacters       int
	MaxContextCapabilities     int
	Memories                   []MemoryView
	CompletionValidator        CompletionValidator
	MaxModelTokens             int64
	MaxModelOutputTokens       int
	MaxStagnantRounds          int
	MaxConcurrentTools         int
}

func (r *AgentRuntime) defaults() {
	if r.ConnectionID == "" {
		r.ConnectionID = NewID()
	}
	if r.MaxModelRounds == 0 {
		r.MaxModelRounds = 20
	}
	if r.MaxToolCalls == 0 {
		r.MaxToolCalls = 40
	}
	if r.MaxRepeatedCall == 0 {
		r.MaxRepeatedCall = 2
	}
	if r.MaxContextCharacters == 0 {
		r.MaxContextCharacters = 80000
	}
	if r.MaxStagnantRounds == 0 {
		r.MaxStagnantRounds = 8
	}
	if r.MaxConcurrentTools == 0 {
		r.MaxConcurrentTools = 4
	}
	if r.Grants == nil {
		r.Grants = map[string]bool{}
	}
}

// NewState creates an independent checkpoint and validates the initial instruction.
func (r *AgentRuntime) NewState(instruction, runID string) (*RuntimeState, error) {
	r.defaults()
	if strings.TrimSpace(instruction) == "" {
		return nil, errors.New("instruction must not be empty")
	}
	if runID == "" {
		runID = NewID()
	}
	return &RuntimeState{SchemaVersion: 1, RunID: runID, Instruction: instruction, Status: "queued", Facts: []Fact{}, Observations: []Observation{}, ModelObservations: []Observation{}, Decisions: []JSON{}, UsedRefs: []string{}, Repeated: map[string]int{}, LoadedSkills: []string{}, Followups: []string{}, Pending: []Invocation{}}, nil
}

// ReferenceAvailable checks expiry and the connection scope of evidence references.
func ReferenceAvailable(f Fact, connectionID string) bool {
	if f.ExpiresAt != nil && !f.ExpiresAt.After(time.Now().UTC()) {
		return false
	}
	return f.ReferenceScope != "connection" || (f.ConnectionID != nil && *f.ConnectionID == connectionID)
}

// ResolveArgument copies JSON arguments and resolves permitted references from complete facts.
func ResolveArgument(value any, facts map[string]Fact, connectionID string, check bool) (any, error) {
	switch t := value.(type) {
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			v, e := ResolveArgument(x, facts, connectionID, check)
			if e != nil {
				return nil, e
			}
			out[i] = v
		}
		return out, nil
	case map[string]any:
		_, idRef := t["$fact_id"]
		_, valueRef := t["$fact_value"]
		if !idRef && !valueRef {
			out := make(JSON, len(t))
			for k, x := range t {
				v, e := ResolveArgument(x, facts, connectionID, check)
				if e != nil {
					return nil, e
				}
				out[k] = v
			}
			return out, nil
		}
		if len(t) != 1 {
			return nil, errors.New("fact_reference_invalid")
		}
		var id string
		var path []any
		if idRef {
			var ok bool
			id, ok = t["$fact_id"].(string)
			if !ok {
				return nil, errors.New("fact_reference_invalid")
			}
		} else {
			ref, ok := t["$fact_value"].(map[string]any)
			if !ok || len(ref) != 2 {
				return nil, errors.New("fact_reference_invalid")
			}
			id, ok = ref["fact_id"].(string)
			if !ok {
				return nil, errors.New("fact_reference_invalid")
			}
			path, ok = ref["path"].([]any)
			if !ok || len(path) > 16 {
				return nil, errors.New("fact_reference_invalid")
			}
		}
		if !validUUID(id) {
			return nil, errors.New("fact_reference_invalid")
		}
		fact, ok := facts[id]
		if !ok {
			return nil, errors.New("fact_reference_not_available")
		}
		if check && !ReferenceAvailable(fact, connectionID) {
			return nil, errors.New("fact_reference_expired")
		}
		if idRef {
			return id, nil
		}
		selected, err := valueAt(modelFactValue(fact), path)
		if err != nil {
			return nil, errors.New("fact_reference_path_invalid")
		}
		raw, err := CanonicalJSON(selected)
		if err != nil {
			return nil, errors.New("fact_reference_path_invalid")
		}
		var copied any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if err := dec.Decode(&copied); err != nil {
			return nil, errors.New("fact_reference_path_invalid")
		}
		return copied, nil
	default:
		return value, nil
	}
}
func valueAt(value any, path []any) (any, error) {
	cur := value
	for _, p := range path {
		switch v := cur.(type) {
		case map[string]any:
			key, ok := p.(string)
			if !ok {
				return nil, errors.New("invalid")
			}
			next, exists := v[key]
			if !exists {
				return nil, errors.New("invalid")
			}
			cur = next
		case []any:
			i, ok := pathIndex(p)
			if !ok || i < 0 || i >= len(v) {
				return nil, errors.New("invalid")
			}
			cur = v[i]
		default:
			return nil, errors.New("invalid")
		}
	}
	return cur, nil
}
func pathIndex(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case json.Number:
		i, e := x.Int64()
		return int(i), e == nil
	case float64:
		if x >= 0 && x == math.Trunc(x) {
			return int(x), true
		}
	}
	return 0, false
}
func factView(f Fact, budget int) FactView {
	f.Value = modelFactValue(f)
	omitted := [][]any{}
	var visit func(any, []any, int) any
	visit = func(value any, path []any, n int) any {
		if n < 4 {
			omitted = append(omitted, append([]any{}, path...))
			switch value.(type) {
			case map[string]any:
				return JSON{}
			case []any:
				return []any{}
			default:
				return nil
			}
		}
		raw, err := CanonicalJSON(value)
		if err != nil {
			omitted = append(omitted, append([]any{}, path...))
			return nil
		}
		if utf8.RuneCount(raw) <= n {
			return value
		}
		switch t := value.(type) {
		case map[string]any:
			out := JSON{}
			remaining := n - 2
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for i, k := range keys {
				keyJSON, err := CanonicalJSON(k)
				if err != nil {
					break
				}
				overhead := utf8.RuneCount(keyJSON) + 2
				slots := len(keys) - i
				if remaining < overhead+4 {
					break
				}
				p := append(append([]any{}, path...), k)
				preview := visit(t[k], p, max(4, (remaining-overhead)/slots))
				// Omitted object fields must not look like provider-reported nulls
				// or empty containers. Keep real null/empty values and array indices.
				omittedValue := preview == nil && t[k] != nil
				switch v := preview.(type) {
				case map[string]any:
					original, ok := t[k].(map[string]any)
					omittedValue = ok && len(v) == 0 && len(original) > 0
				case []any:
					original, ok := t[k].([]any)
					omittedValue = ok && len(v) == 0 && len(original) > 0
				}
				if omittedValue {
					continue
				}
				enc, err := CanonicalJSON(preview)
				if err != nil {
					break
				}
				cost := overhead + utf8.RuneCount(enc)
				if cost > remaining {
					break
				}
				out[k] = preview
				remaining -= cost
			}
			if len(out) != len(t) {
				omitted = append(omitted, append([]any{}, path...))
			}
			return out
		case []any:
			out := []any{}
			remaining := n - 2
			limit := min(len(t), 32)
			for i := 0; i < limit; i++ {
				if remaining < 5 {
					break
				}
				p := append(append([]any{}, path...), i)
				preview := visit(t[i], p, max(4, (remaining-1)/(limit-i)))
				enc, err := CanonicalJSON(preview)
				if err != nil {
					break
				}
				cost := 1 + utf8.RuneCount(enc)
				if cost > remaining {
					break
				}
				out = append(out, preview)
				remaining -= cost
			}
			if len(out) != len(t) {
				omitted = append(omitted, append([]any{}, path...))
			}
			return out
		default:
			omitted = append(omitted, append([]any{}, path...))
			return nil
		}
	}
	preview := visit(f.Value, []any{}, budget)
	f.Value, _ = preview.(map[string]any)
	if f.Value == nil {
		f.Value = JSON{}
	}
	return FactView{Fact: f, OmittedPaths: omitted, ReferenceAvailable: true}
}
func arrayOmissions(facts []Fact, views []FactView) []string {
	notes := []string{}
	seen := map[string]bool{}
	hasOmissions := false
	for i := len(facts) - 1; i >= 0; i-- {
		hasOmissions = hasOmissions || len(views[i].OmittedPaths) > 0
		for _, path := range views[i].OmittedPaths {
			selected, err := valueAt(modelFactValue(facts[i]), path)
			if err != nil {
				continue
			}
			var walk func(any, []any)
			walk = func(v any, p []any) {
				if len(notes) >= 12 {
					return
				}
				switch t := v.(type) {
				case []any:
					key := facts[i].FactID + fmt.Sprint(p)
					if !seen[key] {
						seen[key] = true
						raw, err := CanonicalJSON(p)
						if err != nil {
							return
						}
						notes = append(notes, fmt.Sprintf("fact %s: array at %s has %d items; omitted null placeholders/empty previews unknown; inspect_fact", facts[i].FactID, raw, len(t)))
					}
					for j := 0; j < min(3, len(t)); j++ {
						walk(t[j], append(append([]any{}, p...), j))
					}
				case map[string]any:
					keys := make([]string, 0, len(t))
					for k := range t {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						walk(t[k], append(append([]any{}, p...), k))
					}
				}
			}
			walk(selected, path)
		}
	}
	if hasOmissions && len(notes) == 0 {
		notes = append(notes, "fact omitted_paths are unknown/incomplete; inspect_fact before asserting null/empty values")
	}
	return notes
}
func (r *AgentRuntime) contextCandidate(state *RuntimeState) ContextPacket {
	r.defaults()
	caps := []JSON{}
	names, catalogTotal, searchResults := selectedCapabilityNames(r.Provider.Capabilities(), r.Grants, state.Instruction+" "+strings.Join(state.Followups, " "), state, r.MaxContextCapabilities)
	for _, name := range names {
		cap := r.Provider.Capabilities()[name]
		if !r.Grants[cap.Name] {
			continue
		}
		v := cap.ModelView()
		v["authorized"] = true
		caps = append(caps, v)
	}
	skillViews := []JSON{}
	skillNames := make([]string, 0, len(r.Provider.Skills()))
	for name := range r.Provider.Skills() {
		skillNames = append(skillNames, name)
	}
	sort.Strings(skillNames)
	for _, name := range skillNames {
		if !r.skillAllowed(name) {
			continue
		}
		s := r.Provider.Skills()[name]
		skillViews = append(skillViews, JSON{"name": s.Description.Name, "description": s.Description.Description})
	}
	obs := []Observation{}
	omissions := []string{}
	if r.MaxContextCapabilities > 0 {
		if catalogTotal > len(caps) {
			omissions = append(omissions, fmt.Sprintf("capability catalog: showing %d of %d authorized capabilities; use search_capabilities to find omitted capabilities", len(caps), catalogTotal))
		}
		if state.CapabilitySearchQuery != "" && len(searchResults) == 0 {
			omissions = append(omissions, "capability search returned no authorized matches for: "+state.CapabilitySearchQuery)
		}
	}
	modelObservations := currentEvidenceObservations(state, state.ModelObservations)
	start := max(0, len(modelObservations)-12)
	for _, o := range modelObservations[start:] {
		raw, err := CanonicalJSON(o.Arguments)
		if err != nil || utf8.RuneCount(raw) > 2000 {
			o.Arguments = JSON{}
			o.ArgumentsOmitted = true
		}
		obs = append(obs, o)
	}
	if start > 0 {
		omissions = append(omissions, fmt.Sprintf("observations: %d older entries", start))
	}
	views := make([]FactView, 0, len(state.Facts))
	for _, f := range state.Facts {
		v := factView(f, 6000)
		v.ReferenceAvailable = ReferenceAvailable(f, r.ConnectionID)
		views = append(views, v)
	}
	notes := arrayOmissions(state.Facts, views)
	omissions = append(omissions, notes...)
	loaded := map[string]string{}
	for _, name := range state.LoadedSkills {
		if s, ok := r.Provider.Skills()[name]; ok && r.skillAllowed(name) {
			loaded[name] = s.Content
		}
	}
	var inspected JSON
	if state.InspectedCapability != nil {
		if c, ok := r.Provider.Capabilities()[*state.InspectedCapability]; ok && r.Grants[c.Name] {
			if view, err := objectOf(c); err == nil {
				inspected = view
			}
		}
	}
	features := []string{}
	if r.Durable {
		features = append(features, "durable_execution")
	}
	if r.MaxContextCapabilities > 0 {
		features = append(features, "capability_search")
	}
	packet := ContextPacket{OriginPackID: r.OriginPackID, Schema: "agenstra.context.v1", Instruction: state.Instruction, Capabilities: caps, Facts: views, Observations: obs, RoundIndex: state.RoundsUsed, RoundsRemaining: r.MaxModelRounds - state.RoundsUsed, ToolCallsRemaining: r.MaxToolCalls - state.ToolCallsUsed, Skills: skillViews, LoadedSkills: loaded, InspectedCapability: inspected, InspectedFact: state.InspectedFact, Followups: state.Followups, RuntimeFeatures: features, ContextOmissions: omissions, Memories: append([]MemoryView{}, r.Memories...)}
	if r.MaxContextCapabilities > 0 {
		packet.CapabilityCatalogTotal = catalogTotal
		packet.CapabilitySearchQuery = state.CapabilitySearchQuery
		packet.CapabilitySearchResults = searchResults
	}
	if r.MaxModelTokens > 0 {
		packet.ModelTokensRemaining = max(0, r.MaxModelTokens-state.ModelUsage.BudgetTokens)
	}
	packet.MaxModelOutputTokens = r.MaxModelOutputTokens
	packet.Progress = runProgress(state, r.MaxStagnantRounds)
	packet.ActionOutcomes = actionOutcomes(state, r.Provider.Capabilities())
	return packet
}

// Context returns an independent bounded model view of the run.
// An invalid JSON state produces an empty view; Step reports execution failures.
func (r *AgentRuntime) Context(state *RuntimeState) ContextPacket {
	if _, err := json.Marshal(state); err != nil {
		return ContextPacket{}
	}
	packet := r.contextCandidate(state)
	projected, _, _ := r.characterProjection(state, packet, r.systemPrompt())
	// A context view may be retained or modified by the host without changing
	// the full evidence used to resolve references in later rounds.
	view, err := cloneJSON(projected)
	if err != nil {
		return ContextPacket{}
	}
	return view
}

// Reject records a failed model observation without dispatching a provider call.
func Reject(state *RuntimeState, callRef, capability, code string, args map[string]any, factID string) {
	if args == nil {
		args = JSON{}
	}
	obs := Observation{CallRef: callRef, Capability: capability, Status: "rejected", ErrorCode: strptr(code), FactID: strptr(factID), Arguments: args}
	state.Observations = append(state.Observations, obs)
	state.ModelObservations = append(state.ModelObservations, obs)
}

// Reject records a failed model observation without dispatching the call.
func (r *AgentRuntime) Reject(state *RuntimeState, callRef, capability, code string, args map[string]any, factID string) {
	Reject(state, callRef, capability, code, args, factID)
}
func deterministicInvocationID(runID, ref string) string {
	ns := [16]byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	h := sha1.New()
	_, _ = h.Write(ns[:])
	_, _ = h.Write([]byte(runID + ":" + ref))
	sum := h.Sum(nil)
	sum[6] = (sum[6] & 15) | 80
	sum[8] = (sum[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// Step performs one bounded decision round and prepares calls without invoking them.
// The driver may checkpoint a model reservation through beforeModel.
func (r *AgentRuntime) Step(ctx context.Context, state *RuntimeState, beforeModel func() error) error {
	r.defaults()
	if err := r.contextPolicy(state).Validate(); err != nil {
		return err
	}
	if len(state.Pending) > 0 || (state.Status != "queued" && state.Status != "running") {
		return nil
	}
	// Validate before walking evidence or computing projection budgets.
	if _, err := json.Marshal(state); err != nil {
		state.Status = "failed"
		state.ErrorCode = strptr("run_state_invalid")
		return nil
	}
	state.Status = "running"
	if state.RoundsUsed >= r.MaxModelRounds {
		state.Status = "failed"
		state.ErrorCode = strptr("model_round_budget_exhausted")
		return nil
	}
	if updateProgress(state, r.MaxStagnantRounds, r.Provider.Capabilities()) {
		state.Status = "failed"
		state.ErrorCode = strptr("agent_stagnated")
		return nil
	}
	feedback := ""
	var decision Decision
	var review *Decision
	for attempt := 0; attempt < 2; attempt++ {
		if state.RoundsUsed >= r.MaxModelRounds {
			state.Status = "failed"
			state.ErrorCode = strptr("model_round_budget_exhausted")
			return nil
		}
		if r.MaxModelTokens > 0 && state.ModelUsage.BudgetTokens >= r.MaxModelTokens {
			state.Status = "failed"
			state.ErrorCode = strptr("model_token_budget_exhausted")
			return nil
		}
		packet := r.contextCandidate(state)
		packet.CompletionReview = review
		if _, err := CanonicalJSON(packet); err != nil {
			state.Status = "failed"
			state.ErrorCode = strptr("run_state_invalid")
			return nil
		}
		prompt := r.systemPrompt() + feedback
		purpose := "decision"
		if len(packet.ActionOutcomes) > 0 {
			prompt += "\n" + actionOutcomePrompt
		}
		if review != nil {
			purpose = "completion_review"
			prompt += "\n" + completionReviewPrompt
		}
		if packet.Progress != nil {
			prompt += "\n" + progressUsagePrompt
		}
		for _, note := range packet.ContextOmissions {
			if strings.HasPrefix(note, "fact ") && strings.Contains(note, "array at") {
				extra := "\nAuthoritative full array lengths from stored Facts follow. A preview may show fewer items; inspect omitted indices before claiming coverage:\n" + note
				if contextCharacters(packet) <= r.MaxContextCharacters-utf8.RuneCountInString(prompt)-utf8.RuneCountInString(extra) {
					prompt += extra
				}
				break
			}
		}
		if len(state.ModelObservations) > 0 {
			last := state.ModelObservations[len(state.ModelObservations)-1]
			if last.ErrorCode != nil && *last.ErrorCode == "repeated_equivalent_call" && last.FactID != nil {
				prompt += "\nYour previous call repeated a completed calculation. Use existing Fact " + *last.FactID + " to answer or inspect its needed path. Do not issue another equivalent tool call."
			}
		}
		if state.InspectedFact != nil {
			if arrayLength, ok := pathIndex(state.InspectedFact["array_length"]); ok {
				if preview, ok := state.InspectedFact["preview"].(map[string]any); ok {
					if shown, ok := preview["value"].([]any); ok && len(shown) < arrayLength {
						prompt += fmt.Sprintf("\nThe inspected array has %d items, but its preview shows only %d. Inspect missing indices before reporting full coverage.", arrayLength, len(shown))
					}
				}
			}
			if parentLength, ok := pathIndex(state.InspectedFact["parent_array_length"]); ok {
				if inspectedIndex, ok := pathIndex(state.InspectedFact["inspected_index"]); ok {
					prompt += fmt.Sprintf("\nThe item you inspected at index %d belongs to an array with %d items. Report the total as %d; do not use the preview length as the total.", inspectedIndex, parentLength, parentLength)
				}
			}
		}
		candidate := packet
		packet, reason, targetMet := r.characterProjection(state, packet, prompt)
		packet, window, tokenLimit, reserve, measurement, measureErr := r.tokenProjection(state, packet, prompt)
		if measureErr == nil && contextCharacters(packet)+utf8.RuneCountInString(prompt) > r.MaxContextCharacters {
			packet = budgetContext(packet, state, r.MaxContextCharacters-utf8.RuneCountInString(prompt))
			packet, window, tokenLimit, reserve, measurement, measureErr = r.tokenProjection(state, packet, prompt)
		}
		if tokenLimit != nil {
			packet.MaxModelInputTokens = *tokenLimit
		}
		candidate.MaxModelInputTokens = packet.MaxModelInputTokens
		candidate.MaxModelOutputTokens = packet.MaxModelOutputTokens
		state.ContextTelemetry = measureContext(state, prompt, candidate, packet, r.MaxContextCharacters)
		state.ContextTelemetry.Policy = r.contextPolicy(state)
		state.ContextTelemetry.Strategy = "projection"
		state.ContextTelemetry.ProjectionReason = reason
		state.ContextTelemetry.TargetMet = targetMet
		state.ContextTelemetry.PolicyUnit = "characters"
		c := state.ContextTelemetry
		if c.Policy.TriggerRatio > 0 && c.ProjectionReason != "none" {
			c.TargetMet = float64(c.InputCharacters) <= float64(c.CharacterLimit)*c.Policy.TargetRatio
		}
		c.ModelContextWindowTokens = window
		c.EffectiveInputTokenLimit = tokenLimit
		c.ReservedOutputTokens = reserve
		if measureErr != nil {
			state.Status = "failed"
			state.ErrorCode = strptr(modelErrorCode(measureErr))
			return nil
		}
		if tokenLimit != nil {
			c.PolicyUnit = "tokens"
			c.TargetMet = measurement.TargetMet
			if measurement.ProjectionReason != "none" {
				c.ProjectionReason = measurement.ProjectionReason
			}
		}
		c.InputTokens = &measurement.Tokens
		c.TokenMeasurementSource = measurement.Source
		if tokenLimit != nil {
			remaining := max(int64(0), *tokenLimit-measurement.Tokens)
			ratio := float64(measurement.Tokens) / float64(*tokenLimit)
			c.TokensRemaining = &remaining
			c.TokenUtilization = &ratio
			c.OverLimit = c.OverLimit || measurement.Tokens > *tokenLimit
		}
		raw, err := CanonicalJSON(packet)
		if err != nil {
			state.Status = "failed"
			state.ErrorCode = strptr("run_state_invalid")
			return nil
		}
		// Detach nested evidence, observations and followups before handing the
		// packet to a model implementation supplied by the host.
		modelPacket, err := cloneJSON(packet)
		if err != nil {
			state.Status = "failed"
			state.ErrorCode = strptr("run_state_invalid")
			return nil
		}
		if state.ContextTelemetry.OverLimit {
			state.Status = "failed"
			state.ErrorCode = strptr("context_too_large")
			return nil
		}
		state.RoundsUsed++
		reservation := ModelCallMetrics{Purpose: purpose, Reservation: true, Round: state.RoundsUsed, Attempts: 1, EstimatedInputTokens: int64(len(raw) + len(prompt) + 128), EstimatedOutputTokens: int64(r.MaxModelOutputTokens), ErrorCode: strptr("model_outcome_unknown")}
		if r.MaxModelTokens > 0 {
			reservation.EstimatedInputTokens = r.MaxModelTokens - state.ModelUsage.BudgetTokens
			reservation.EstimatedOutputTokens = 0
		}
		recordModelCall(state, reservation)
		callIndex := len(state.ModelCalls) - 1
		if beforeModel != nil {
			if err := beforeModel(); err != nil {
				state.ModelCalls[callIndex].Attempts = 0
				rebuildModelUsage(state)
				return err
			}
		}
		started := time.Now()
		d, err := r.Model.Decide(ctx, modelPacket, prompt)
		metrics := ModelCallMetrics{Attempts: 1, EstimatedInputTokens: int64(len(raw) + len(prompt) + 128), ElapsedMilliseconds: time.Since(started).Milliseconds()}
		if d.ModelCall != nil {
			metrics = *d.ModelCall
		}
		metrics.FormatRecovery = attempt > 0
		metrics.Round = state.RoundsUsed
		metrics.Purpose = purpose
		metrics.Reservation = false
		if metrics.UsageAvailable && state.ContextTelemetry != nil {
			n := metrics.InputTokens
			state.ContextTelemetry.ReportedInputTokens = &n
		}
		if err != nil {
			metrics.ErrorCode = strptr(modelErrorCode(err))
		}
		state.ModelCalls[callIndex] = metrics
		rebuildModelUsage(state)
		if r.MaxModelTokens > 0 && state.ModelUsage.BudgetTokens > r.MaxModelTokens {
			state.Status = "failed"
			state.ErrorCode = strptr("model_token_budget_exhausted")
			return nil
		}
		if d.Schema == "" {
			d.Schema = "agenstra.decision.v1"
		}
		if err == nil {
			err = d.Validate()
		}
		if err == nil && review != nil && d.Kind != "final" {
			err = ModelDecisionError{"model_decision_invalid"}
			state.ModelCalls[callIndex].ErrorCode = strptr("model_decision_invalid")
			rebuildModelUsage(state)
		}
		if err == nil {
			if review == nil && d.Kind == "final" && len(packet.ActionOutcomes) > 0 {
				// Save the proposal for audit/checkpointing, but never publish it.
				// The reviewer has no path to executing another business action.
				recordDecision(state, d)
				review = &d
				feedback = ""
				attempt = -1
				continue
			}
			decision = d
			break
		}
		code := modelErrorCode(err)
		if code != "model_decision_invalid" {
			state.Status = "failed"
			state.ErrorCode = strptr(code)
			return nil
		}
		if attempt == 1 || state.RoundsUsed >= r.MaxModelRounds {
			state.Status = "failed"
			state.ErrorCode = strptr("model_decision_invalid")
			return nil
		}
		var oversized DecisionTooManyCallsError
		if review != nil {
			feedback = "\nYour previous review was invalid. Return exactly one final agenstra.decision.v1 JSON decision; no tools or other decision kinds are allowed during completion review."
		} else if errors.As(err, &oversized) {
			feedback = "\nYour previous decision was invalid: tool_batch.calls has at most 4 items. Return one valid agenstra.decision.v1 JSON decision with no more than 4 calls."
		} else {
			feedback = "\nYour previous response was not a valid agenstra.decision.v1 JSON decision. Return exactly one valid decision object. Do not put read_skill, inspect_capability, or inspect_fact inside tool_batch.calls."
		}
	}
	recordDecision(state, decision)
	facts := map[string]Fact{}
	for _, f := range state.Facts {
		facts[f.FactID] = f
	}
	switch decision.Kind {
	case "search_capabilities":
		if r.MaxContextCapabilities <= 0 {
			Reject(state, "search", "agent.search_capabilities", "capability_search_disabled", nil, "")
			break
		}
		state.CapabilitySearchQuery = decision.Query
		state.CapabilitySearchResults = searchAuthorizedCapabilities(r.Provider.Capabilities(), r.Grants, decision.Query, r.MaxContextCapabilities)
	case "inspect_fact":
		selected, err := ResolveArgument(JSON{"$fact_value": JSON{"fact_id": decision.FactID, "path": decision.Path}}, facts, r.ConnectionID, false)
		if err != nil {
			state.InspectedFact = JSON{"error_code": err.Error()}
			break
		}
		fact := facts[decision.FactID]
		key := browserReadProgressKeys(state, r.Provider.Capabilities())[fact.FactID]
		if key == "" {
			key = progressKey(fact.Value)
		}
		recordInspection(state, JSON{"fact": key, "path": decision.Path})
		fact.Value = JSON{"value": selected}
		fact.ModelOutput = nil
		view := factView(fact, 6000)
		state.InspectedFact = JSON{"fact_id": decision.FactID, "path": decision.Path, "preview": view.Value, "omitted_paths": view.OmittedPaths}
		if a, ok := selected.([]any); ok {
			state.InspectedFact["array_length"] = len(a)
		}
		for i := len(decision.Path) - 1; i >= 0; i-- {
			if index, ok := pathIndex(decision.Path[i]); ok {
				parent, e := valueAt(facts[decision.FactID].Value, decision.Path[:i])
				if e == nil {
					if a, ok := parent.([]any); ok {
						state.InspectedFact["parent_array_length"] = len(a)
						state.InspectedFact["inspected_index"] = index
					}
				}
				break
			}
		}
	case "inspect_capability":
		state.InspectedCapability = nil
		if _, ok := r.Provider.Capabilities()[decision.Name]; !ok || !r.Grants[decision.Name] {
			Reject(state, "inspect", "agent.inspect_capability", "capability_unknown", nil, "")
		} else {
			state.InspectedCapability = &decision.Name
			recordInspection(state, JSON{"capability": decision.Name})
		}
	case "read_skill":
		if _, ok := r.Provider.Skills()[decision.Name]; !ok || !r.skillAllowed(decision.Name) {
			Reject(state, "skill", "agent.read_skill", "skill_unknown", nil, "")
		} else {
			next := []string{}
			for _, s := range state.LoadedSkills {
				if s != decision.Name {
					next = append(next, s)
				}
			}
			state.LoadedSkills = append(next, decision.Name)
		}
	case "final":
		valid := true
		if len(facts) > 0 && len(decision.FactIDs) == 0 {
			valid = false
		}
		for _, id := range decision.FactIDs {
			if _, ok := facts[id]; !ok {
				valid = false
			}
		}
		refs, refsErr := resolveResultRefs(decision.ResultRefs, decision.FactIDs, facts, r.ConnectionID)
		if !valid {
			Reject(state, "final", "agent.final", "final_fact_citations_invalid", JSON{"feedback": "Cite at least one observed Fact when Facts exist. Every fact_ids entry must be an existing Fact ID from this run; use inspect_fact to read evidence before correcting the answer."}, "")
		} else if refsErr != nil {
			Reject(state, "final", "agent.final", "final_result_refs_invalid", JSON{"feedback": "Each result_refs entry must name an available Fact included in fact_ids and a path to a nonempty scalar business ID (string or integer). Inspect the Fact and use its actual path, including data and, for browser receipts, result. Omit result_refs when the answer needs no object reference; never invent an ID."}, "")
		} else if r.CompletionValidator != nil {
			err := r.CompletionValidator(ctx, CompletionContext{RunID: state.RunID, OriginPackID: r.OriginPackID, Instruction: state.Instruction, AnswerMarkdown: decision.AnswerMarkdown, FactIDs: decision.FactIDs, Facts: state.Facts, Observations: state.Observations, Followups: state.Followups})
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				code, feedback := "completion_validation_failed", "The final answer did not satisfy the host's completion checks."
				var validation CompletionValidationError
				if errors.As(err, &validation) {
					if safeCodePattern.MatchString(validation.Kind) {
						code = validation.Kind
					}
					if len(validation.Feedback) <= 1000 && validation.Feedback != "" {
						feedback = validation.Feedback
					}
				}
				Reject(state, "final", "agent.final", code, JSON{"feedback": feedback}, "")
			} else {
				state.Status = "completed"
				state.AnswerMarkdown = decision.AnswerMarkdown
				state.ResultRefs = refs
			}
		} else {
			state.Status = "completed"
			state.AnswerMarkdown = decision.AnswerMarkdown
			state.ResultRefs = refs
		}
	case "request_input":
		state.Status = "needs_input"
		state.InputField = &decision.Field
		state.InputPrompt = &decision.Prompt
		state.InputSchema = decision.InputSchema
	case "tool_batch":
		for _, call := range decision.Calls {
			kind := strings.TrimPrefix(call.Capability, "agent.")
			if kind == "read_skill" || kind == "inspect_capability" || kind == "inspect_fact" {
				Reject(state, call.CallRef, call.Capability, "use_"+kind+"_decision", call.Arguments, "")
				continue
			}
			used := false
			for _, ref := range state.UsedRefs {
				if ref == call.CallRef {
					used = true
				}
			}
			if used {
				Reject(state, call.CallRef, call.Capability, "tool_call_ref_reused", call.Arguments, "")
				continue
			}
			state.UsedRefs = append(state.UsedRefs, call.CallRef)
			args := JSON{}
			var resolveErr error
			for k, v := range call.Arguments {
				args[k], resolveErr = ResolveArgument(v, facts, r.ConnectionID, true)
				if resolveErr != nil {
					break
				}
			}
			if resolveErr != nil {
				Reject(state, call.CallRef, call.Capability, resolveErr.Error(), call.Arguments, "")
				continue
			}
			resolved := call
			resolved.Arguments = args
			digest := ArgumentsDigest(resolved)
			cap, exists := r.Provider.Capabilities()[call.Capability]
			if exists && cap.Effect == "compute" {
				repeat := false
				for _, p := range state.Pending {
					if ArgumentsDigest(p.Call) == digest {
						repeat = true
					}
				}
				if repeat {
					Reject(state, call.CallRef, call.Capability, "repeated_equivalent_call", call.Arguments, "")
					continue
				}
				failed := map[string]bool{}
				for _, o := range state.Observations {
					if o.ErrorCode != nil && *o.ErrorCode == "operation_failed" {
						failed[o.CallRef] = true
					}
				}
				var prior string
				for i := len(state.Observations) - 1; i >= 0; i-- {
					o := state.Observations[i]
					if o.Status == "succeeded" && o.Capability == call.Capability && !failed[o.CallRef] && o.FactID != nil {
						if f, ok := facts[*o.FactID]; ok && ReferenceAvailable(f, r.ConnectionID) {
							compare := resolved
							compare.Arguments = o.Arguments
							if ArgumentsDigest(compare) == digest {
								prior = f.FactID
								break
							}
						}
					}
				}
				if prior != "" {
					Reject(state, call.CallRef, call.Capability, "repeated_equivalent_call", call.Arguments, prior)
					continue
				}
			}
			if state.Repeated == nil {
				state.Repeated = map[string]int{}
			}
			// Browser observations and host reads can change after a page/business
			// write. Let declared read actions fetch current state again; writes
			// retain duplicate protection and all round/tool budgets still apply.
			refreshableBrowserRead := call.Capability == "ui.get_context" || call.Capability == "ui.command_status" ||
				(exists && cap.Effect == "read" && strings.HasPrefix(call.Capability, "ui.") && cap.Operation != nil && cap.Operation.PollCapability == "ui.command_status")
			if !refreshableBrowserRead && state.Repeated[digest] >= r.MaxRepeatedCall {
				Reject(state, call.CallRef, call.Capability, "repeated_equivalent_call", call.Arguments, "")
				continue
			}
			state.Repeated[digest]++
			state.Pending = append(state.Pending, Invocation{InvocationID: deterministicInvocationID(state.RunID, call.CallRef), Call: resolved, OriginalArguments: call.Arguments, Status: "prepared"})
		}
		if state.ToolCallsUsed+len(state.Pending) > r.MaxToolCalls {
			state.Pending = []Invocation{}
			state.Status = "failed"
			state.ErrorCode = strptr("tool_call_budget_exhausted")
		} else {
			state.ToolCallsUsed += len(state.Pending)
		}
	}
	return nil
}
func recordDecision(state *RuntimeState, decision Decision) {
	raw, err := CanonicalJSON(decision)
	if err != nil {
		return // Invalid decisions cannot become saved history.
	}
	var stored JSON
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&stored); err != nil {
		return
	}
	state.Decisions = append(state.Decisions, stored)
}

// Result exposes the current result; its mutable slices remain owned by state.
// Copy them before modifying or sharing them with another goroutine.
func (r *AgentRuntime) Result(state *RuntimeState) RunResult {
	status := state.Status
	if status == "queued" || status == "running" {
		status = "failed"
	}
	return RunResult{ContextTelemetry: state.ContextTelemetry, Progress: runProgress(state, r.MaxStagnantRounds), Status: status, AnswerMarkdown: state.AnswerMarkdown, ResultRefs: state.ResultRefs, ErrorCode: state.ErrorCode, InputField: state.InputField, InputPrompt: state.InputPrompt, InputSchema: state.InputSchema, Facts: state.Facts, Observations: state.Observations, Decisions: state.Decisions, ModelCalls: state.ModelCalls, ModelUsage: state.ModelUsage}
}

// Run executes transient work until completion or a required user action.
// It respects cancellation and leaves uncertain external effects for reconciliation.
func (r *AgentRuntime) Run(ctx context.Context, instruction string) (RunResult, error) {
	state, err := r.NewState(instruction, "")
	if err != nil {
		return RunResult{}, err
	}
	for state.Status == "queued" || state.Status == "running" {
		if err := r.Step(ctx, state, nil); err != nil {
			return r.Result(state), err
		}
		if len(state.Pending) > 0 {
			approvalRequired := false
			for _, item := range state.Pending {
				if cap, ok := r.Provider.Capabilities()[item.Call.Capability]; ok && r.Grants[cap.Name] && cap.ApprovalRequired {
					approvalRequired = true
					break
				}
			}
			if approvalRequired {
				state.Status = "needs_approval"
				state.ErrorCode = strptr("durable_host_required")
				break
			}
			if independentBatch(r.Provider, state.Pending, ExecutionPolicy{GrantedCapabilities: r.Grants}, r.MaxConcurrentTools) {
				tasks := make([]parallelInvocation, len(state.Pending))
				for i, item := range state.Pending {
					tasks[i] = parallelInvocation{call: item.Call, grants: r.Grants, inv: InvocationContext{RunID: state.RunID, InvocationID: item.InvocationID, IdempotencyKey: item.InvocationID, OwnerID: "transient", ConnectionID: r.ConnectionID}}
				}
				outcomes := invokeParallel(ctx, r.Provider, tasks, r.MaxConcurrentTools, 0)
				if ctx.Err() != nil {
					return r.Result(state), ctx.Err()
				}
				for i, outcome := range outcomes {
					Observe(state, &state.Pending[i], outcome)
					if outcome.ErrorCode == "provider_outcome_unknown" {
						state.Status = "needs_reconciliation"
						state.ErrorCode = strptr("provider_outcome_unknown")
					}
				}
				state.Pending = []Invocation{}
				continue
			}
			for i := range state.Pending {
				item := &state.Pending[i]
				cap, ok := r.Provider.Capabilities()[item.Call.Capability]
				inv := &InvocationContext{RunID: state.RunID, InvocationID: item.InvocationID, IdempotencyKey: item.InvocationID, OwnerID: "transient", ConnectionID: r.ConnectionID}
				outcome, err := ExecuteCall(ctx, r.Provider, r.Grants, item.Call, inv)
				if err != nil {
					outcome = CallOutcome{ErrorCode: "provider_outcome_unknown"}
				}
				Observe(state, item, outcome)
				if outcome.ErrorCode == "provider_outcome_unknown" || ((outcome.ErrorCode == "upstream_response_invalid" || outcome.ErrorCode == "upstream_unavailable") && ok && cap.Effect != "read") {
					state.Status = "needs_reconciliation"
					state.ErrorCode = strptr("provider_outcome_unknown")
				}
			}
			state.Pending = []Invocation{}
		}
	}
	return r.Result(state), nil
}

// NewState creates a checkpoint using the runtime's default limits.
func NewState(instruction, runID string) (*RuntimeState, error) {
	return (&AgentRuntime{}).NewState(instruction, runID)
}

func (r *AgentRuntime) systemPrompt() string {
	prompt := r.Provider.SystemPrompt() + "\n" + conversationGuidance + "\n" + decisionProtocolPrompt
	if r.MaxContextCapabilities > 0 {
		prompt += "\n" + capabilitySearchPrompt
	}
	if len(r.Memories) > 0 {
		prompt += memoryUsagePrompt
	}
	return prompt
}

func (r *AgentRuntime) skillAllowed(name string) bool {
	if !strings.Contains(name, "::") {
		return true
	}
	for _, cap := range r.Provider.Capabilities() {
		if r.Grants[cap.Name] && slices.Contains(cap.SkillsList, name) {
			return true
		}
	}
	return false
}
