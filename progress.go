package agenstra

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Progress is an evidence-derived view, not a model-authored task plan.
type ProgressItem struct {
	Capability string  `json:"capability"`
	CallRef    string  `json:"call_ref,omitempty"`
	Status     string  `json:"status"`
	FactID     *string `json:"fact_id,omitempty"`
	ErrorCode  *string `json:"error_code,omitempty"`
}

type RunProgress struct {
	Completed         []ProgressItem `json:"completed,omitempty"`
	Pending           []ProgressItem `json:"pending,omitempty"`
	Blocked           []ProgressItem `json:"blocked,omitempty"`
	CompletedCount    int            `json:"completed_count"`
	BlockedCount      int            `json:"blocked_count"`
	OmittedItems      int            `json:"omitted_items"`
	NoProgressRounds  int            `json:"no_progress_rounds"`
	StagnationWarning bool           `json:"stagnation_warning,omitempty"`
}

type ProgressTracker struct {
	Fingerprint      string   `json:"fingerprint"`
	NoProgressRounds int      `json:"no_progress_rounds"`
	Inspections      []string `json:"inspections,omitempty"`
}

func progressKey(value any) string {
	raw, _ := CanonicalJSON(value)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func recordInspection(state *RuntimeState, value any) {
	if state.Progress == nil {
		state.Progress = &ProgressTracker{}
	}
	key := progressKey(value)
	if !slices.Contains(state.Progress.Inspections, key) {
		state.Progress.Inspections = append(state.Progress.Inspections, key)
	}
}

// A completed browser read wraps business data in a new command receipt. The
// delivery ID is not new information, unlike a new write or changed read data.
func browserReadProgressKeys(state *RuntimeState, capabilities map[string]CapabilityDescription) map[string]string {
	facts := map[string]Fact{}
	for _, fact := range state.Facts {
		facts[fact.FactID] = fact
	}
	keys := map[string]string{}
	for _, observation := range currentEvidenceObservations(state, state.Observations) {
		capability, ok := capabilities[observation.Capability]
		if !ok || capability.Effect != "read" || capability.Operation == nil || capability.Operation.PollCapability != "ui.command_status" || observation.ErrorCode != nil || observation.FactID == nil {
			continue
		}
		fact, ok := facts[*observation.FactID]
		if !ok || fact.SourceCapability != "ui.command_status" {
			continue
		}
		data, ok := fact.Value["data"].(map[string]any)
		if !ok || data["status"] != "succeeded" {
			continue
		}
		if _, ok := data["result"]; !ok {
			continue
		}
		keys[fact.FactID] = progressKey(JSON{"capability": capability.Name, "result": data["result"]})
	}
	return keys
}

func updateProgress(state *RuntimeState, limit int, capabilities map[string]CapabilityDescription) bool {
	if state.Progress == nil {
		state.Progress = &ProgressTracker{}
	}
	// Deduplicate content: new call refs or Fact IDs alone are not progress.
	keys := map[string]bool{}
	readKeys := browserReadProgressKeys(state, capabilities)
	for _, fact := range state.Facts {
		key, ok := readKeys[fact.FactID]
		if !ok {
			key = progressKey(JSON{"capability": fact.SourceCapability, "value": fact.Value})
		}
		keys[key] = true
	}
	for _, skill := range state.LoadedSkills {
		keys["skill:"+skill] = true
	}
	for _, inspection := range state.Progress.Inspections {
		keys["inspection:"+inspection] = true
	}
	for _, followup := range state.Followups {
		keys["input:"+progressKey(followup)] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	fingerprint := progressKey(ordered)
	if state.Progress.Fingerprint == fingerprint {
		state.Progress.NoProgressRounds++
	} else {
		state.Progress.Fingerprint = fingerprint
		state.Progress.NoProgressRounds = 0
	}
	return state.Progress.NoProgressRounds >= limit
}

func runProgress(state *RuntimeState, limit int) *RunProgress {
	if len(state.Observations) == 0 && len(state.Pending) == 0 && len(state.Decisions) == 0 && (state.Progress == nil || state.Progress.NoProgressRounds == 0) {
		return nil
	}
	view := &RunProgress{}
	if state.Progress != nil {
		view.NoProgressRounds = state.Progress.NoProgressRounds
		view.StagnationWarning = view.NoProgressRounds >= max(2, limit-2)
	}
	active := map[string]bool{}
	for _, item := range state.Pending {
		if item.Status != "succeeded" && item.Status != "failed" {
			active[item.Call.CallRef] = true
			view.Pending = append(view.Pending, ProgressItem{Capability: item.Call.Capability, CallRef: item.Call.CallRef, Status: item.Status, ErrorCode: item.ErrorCode})
		}
	}
	observations := currentEvidenceObservations(state, state.Observations)
	latest := map[string]int{}
	for i, obs := range observations {
		latest[obs.CallRef] = i
	}
	// Keep the last successful result of each capability so a busy recent tool
	// does not crowd earlier work out of the observation window.
	completed := map[string]ProgressItem{}
	blocked := map[string]ProgressItem{}
	for i, obs := range observations {
		if latest[obs.CallRef] != i || active[obs.CallRef] {
			continue
		}
		item := ProgressItem{Capability: obs.Capability, CallRef: obs.CallRef, Status: obs.Status, FactID: obs.FactID, ErrorCode: obs.ErrorCode}
		if obs.Status == "succeeded" {
			view.CompletedCount++
			completed[obs.Capability] = item
			delete(blocked, obs.Capability)
		} else {
			view.BlockedCount++
			blocked[obs.Capability] = item
		}
	}
	project := func(items map[string]ProgressItem) []ProgressItem {
		out := make([]ProgressItem, 0, len(items))
		for _, item := range items {
			out = append(out, item)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Capability < out[j].Capability })
		view.OmittedItems += max(0, len(out)-8)
		return out[:min(8, len(out))]
	}
	view.Completed = project(completed)
	view.Blocked = project(blocked)
	return view
}

// Polling replaces the initial receipt Fact in active state. Keep the immutable
// audit observations, but direct model views to the retained result of that same
// invocation rather than an unavailable queued receipt.
func currentEvidenceObservations(state *RuntimeState, observations []Observation) []Observation {
	facts := map[string]bool{}
	for _, fact := range state.Facts {
		facts[fact.FactID] = true
	}
	polls := map[string]Observation{}
	for _, observation := range state.Observations {
		if observation.FactID == nil || !facts[*observation.FactID] || observation.ErrorCode != nil || !strings.HasPrefix(observation.CallRef, "poll-") {
			continue
		}
		end := strings.LastIndex(observation.CallRef, "-")
		if end > len("poll-") {
			polls[observation.CallRef[len("poll-"):end]] = observation
		}
	}
	result := append([]Observation{}, observations...)
	redundantPolls := map[string]bool{}
	for i, observation := range result {
		if observation.FactID == nil || observation.ErrorCode != nil {
			continue
		}
		if poll, ok := polls[deterministicInvocationID(state.RunID, observation.CallRef)]; ok {
			if facts[*observation.FactID] && *observation.FactID != *poll.FactID {
				continue
			}
			result[i].FactID = poll.FactID
			redundantPolls[poll.CallRef] = true
		}
	}
	// The original action now carries this invocation's current result. Keep a
	// standalone poll when its action is absent, but do not spend two observation
	// slots or count two completions for the same action. Audit state is unchanged.
	projected := make([]Observation, 0, len(result))
	for _, observation := range result {
		if !redundantPolls[observation.CallRef] {
			projected = append(projected, observation)
		}
	}
	return projected
}

const progressUsagePrompt = "progress summarizes observed outcomes; pending is unfinished. Reuse cited Facts. On stagnation_warning, change approach or explain the verified limitation."
