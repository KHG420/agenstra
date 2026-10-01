package agenstra

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type AgentRuntime struct {
	Provider             CapabilityProvider
	Model                DecisionModel
	Grants               map[string]bool
	ConnectionID         string
	Durable              bool
	MaxModelRounds       int
	MaxToolCalls         int
	MaxRepeatedCall      int
	MaxContextCharacters int
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
	if r.Grants == nil {
		r.Grants = map[string]bool{}
	}
}
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
func ReferenceAvailable(f Fact, connectionID string) bool {
	if f.ExpiresAt != nil && !f.ExpiresAt.After(time.Now().UTC()) {
		return false
	}
	return f.ReferenceScope != "connection" || (f.ConnectionID != nil && *f.ConnectionID == connectionID)
}
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
		selected, err := valueAt(fact.Value, path)
		if err != nil {
			return nil, errors.New("fact_reference_path_invalid")
		}
		raw, _ := CanonicalJSON(selected)
		var copied any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		_ = dec.Decode(&copied)
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
		raw, _ := CanonicalJSON(value)
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
				keyJSON, _ := CanonicalJSON(k)
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
				enc, _ := CanonicalJSON(preview)
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
				enc, _ := CanonicalJSON(preview)
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
			selected, err := valueAt(facts[i].Value, path)
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
						raw, _ := CanonicalJSON(p)
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
func (r *AgentRuntime) Context(state *RuntimeState) ContextPacket {
	r.defaults()
	caps := []JSON{}
	names := make([]string, 0, len(r.Provider.Capabilities()))
	for name := range r.Provider.Capabilities() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cap := r.Provider.Capabilities()[name]
		v := cap.ModelView()
		v["authorized"] = cap.Effect == "read" || r.Grants[cap.Name]
		caps = append(caps, v)
	}
	skillViews := []JSON{}
	skillNames := make([]string, 0, len(r.Provider.Skills()))
	for name := range r.Provider.Skills() {
		skillNames = append(skillNames, name)
	}
	sort.Strings(skillNames)
	for _, name := range skillNames {
		s := r.Provider.Skills()[name]
		skillViews = append(skillViews, JSON{"name": s.Description.Name, "description": s.Description.Description})
	}
	obs := []Observation{}
	omissions := []string{}
	start := max(0, len(state.ModelObservations)-12)
	for _, o := range state.ModelObservations[start:] {
		raw, _ := CanonicalJSON(o.Arguments)
		if utf8.RuneCount(raw) > 2000 {
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
		if s, ok := r.Provider.Skills()[name]; ok {
			loaded[name] = s.Content
		}
	}
	var inspected JSON
	if state.InspectedCapability != nil {
		if c, ok := r.Provider.Capabilities()[*state.InspectedCapability]; ok {
			raw, _ := json.Marshal(c)
			_ = json.Unmarshal(raw, &inspected)
		}
	}
	features := []string{}
	if r.Durable {
		features = append(features, "durable_execution")
	}
	packet := ContextPacket{Schema: "agenstra.context.v1", Instruction: state.Instruction, Capabilities: caps, Facts: views, Observations: obs, RoundIndex: state.RoundsUsed, RoundsRemaining: r.MaxModelRounds - state.RoundsUsed, ToolCallsRemaining: r.MaxToolCalls - state.ToolCallsUsed, Skills: skillViews, LoadedSkills: loaded, InspectedCapability: inspected, InspectedFact: state.InspectedFact, Followups: state.Followups, RuntimeFeatures: features, ContextOmissions: omissions}
	available := r.MaxContextCharacters - utf8.RuneCountInString(r.Provider.SystemPrompt())
	return budgetContext(packet, state, available)
}
func Reject(state *RuntimeState, callRef, capability, code string, args map[string]any, factID string) {
	if args == nil {
		args = JSON{}
	}
	obs := Observation{CallRef: callRef, Capability: capability, Status: "rejected", ErrorCode: strptr(code), FactID: strptr(factID), Arguments: args}
	state.Observations = append(state.Observations, obs)
	state.ModelObservations = append(state.ModelObservations, obs)
}
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
func (r *AgentRuntime) Step(ctx context.Context, state *RuntimeState, beforeModel func() error) error {
	r.defaults()
	if len(state.Pending) > 0 || (state.Status != "queued" && state.Status != "running") {
		return nil
	}
	state.Status = "running"
	if state.RoundsUsed >= r.MaxModelRounds {
		state.Status = "failed"
		state.ErrorCode = strptr("model_round_budget_exhausted")
		return nil
	}
	feedback := ""
	var decision Decision
	for attempt := 0; attempt < 2; attempt++ {
		packet := r.Context(state)
		prompt := r.Provider.SystemPrompt() + feedback
		for _, note := range packet.ContextOmissions {
			if strings.HasPrefix(note, "fact ") && strings.Contains(note, "array at") {
				extra := "\nAuthoritative full array lengths from stored Facts follow. A preview may show fewer items; inspect omitted indices before claiming coverage:\n" + note
				raw, _ := CanonicalJSON(packet)
				if utf8.RuneCountInString(prompt)+utf8.RuneCount(raw)+utf8.RuneCountInString(extra) <= r.MaxContextCharacters {
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
		packet = budgetContext(packet, state, r.MaxContextCharacters-utf8.RuneCountInString(prompt))
		raw, _ := CanonicalJSON(packet)
		if utf8.RuneCountInString(prompt)+utf8.RuneCount(raw) > r.MaxContextCharacters {
			state.Status = "failed"
			state.ErrorCode = strptr("context_too_large")
			return nil
		}
		state.RoundsUsed++
		if beforeModel != nil {
			if err := beforeModel(); err != nil {
				return err
			}
		}
		d, err := r.Model.Decide(ctx, packet, prompt)
		if d.Schema == "" {
			d.Schema = "agenstra.decision.v1"
		}
		if err == nil {
			err = d.Validate()
		}
		if err == nil {
			decision = d
			break
		}
		code := ErrorCode(err)
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
		if errors.As(err, &oversized) {
			feedback = "\nYour previous decision was invalid: tool_batch.calls has at most 4 items. Return one valid agenstra.decision.v1 JSON decision with no more than 4 calls."
		} else {
			feedback = "\nYour previous response was not a valid agenstra.decision.v1 JSON decision. Return exactly one valid decision object. Do not put read_skill, inspect_capability, or inspect_fact inside tool_batch.calls."
		}
	}
	raw, _ := CanonicalJSON(decision)
	var stored JSON
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	_ = decoder.Decode(&stored)
	state.Decisions = append(state.Decisions, stored)
	facts := map[string]Fact{}
	for _, f := range state.Facts {
		facts[f.FactID] = f
	}
	switch decision.Kind {
	case "inspect_fact":
		selected, err := ResolveArgument(JSON{"$fact_value": JSON{"fact_id": decision.FactID, "path": decision.Path}}, facts, r.ConnectionID, false)
		if err != nil {
			state.InspectedFact = JSON{"error_code": err.Error()}
			break
		}
		fact := facts[decision.FactID]
		fact.Value = JSON{"value": selected}
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
		state.InspectedCapability = &decision.Name
		if _, ok := r.Provider.Capabilities()[decision.Name]; !ok {
			Reject(state, "inspect", "agent.inspect_capability", "capability_unknown", nil, "")
		}
	case "read_skill":
		if _, ok := r.Provider.Skills()[decision.Name]; !ok {
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
		if !valid {
			Reject(state, "final", "agent.final", "final_fact_citations_invalid", nil, "")
		} else {
			state.Status = "completed"
			state.AnswerMarkdown = decision.AnswerMarkdown
		}
	case "request_input":
		state.Status = "needs_input"
		state.InputField = &decision.Field
		state.InputPrompt = &decision.Prompt
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
			// These reserved browser observations change as handlers update the page
			// or a command progresses. Re-reading them is necessary for a later
			// action in the same run; the normal round/tool budgets still apply.
			refreshableBrowserRead := call.Capability == "ui.get_context" || call.Capability == "ui.command_status"
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
func (r *AgentRuntime) Result(state *RuntimeState) RunResult {
	status := state.Status
	if status == "queued" || status == "running" {
		status = "failed"
	}
	return RunResult{Status: status, AnswerMarkdown: state.AnswerMarkdown, ErrorCode: state.ErrorCode, InputField: state.InputField, InputPrompt: state.InputPrompt, Facts: state.Facts, Observations: state.Observations, Decisions: state.Decisions}
}
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
				if cap, ok := r.Provider.Capabilities()[item.Call.Capability]; ok && cap.ApprovalRequired {
					approvalRequired = true
					break
				}
			}
			if approvalRequired {
				state.Status = "needs_approval"
				state.ErrorCode = strptr("durable_host_required")
				break
			}
			for i := range state.Pending {
				item := &state.Pending[i]
				cap, ok := r.Provider.Capabilities()[item.Call.Capability]
				inv := &InvocationContext{RunID: state.RunID, InvocationID: item.InvocationID, IdempotencyKey: item.InvocationID, OwnerID: "transient", ConnectionID: r.ConnectionID}
				outcome, _ := ExecuteCall(ctx, r.Provider, r.Grants, item.Call, inv)
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
func NewState(instruction, runID string) (*RuntimeState, error) {
	return (&AgentRuntime{}).NewState(instruction, runID)
}
