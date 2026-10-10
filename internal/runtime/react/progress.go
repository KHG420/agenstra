package react

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func progressKey(value any) string {
	raw, err := agentcontract.CanonicalJSON(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func recordInspection(state *agentcontract.RuntimeState, value any) {
	if state.Progress == nil {
		state.Progress = &agentcontract.ProgressTracker{}
	}
	key := progressKey(value)
	if key == "" {
		return
	}
	if !slices.Contains(state.Progress.Inspections, key) {
		state.Progress.Inspections = append(state.Progress.Inspections, key)
	}
}

// A completed browser read wraps business data in a new command receipt. The
// delivery ID is not new information, unlike a new write or changed read data.
func browserReadProgressKeys(state *agentcontract.RuntimeState, capabilities map[string]agentcontract.CapabilityDescription) map[string]string {
	facts := map[string]agentcontract.Fact{}
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
		keys[fact.FactID] = progressKey(agentcontract.JSON{"capability": capability.Name, "result": data["result"]})
	}
	return keys
}

func updateProgress(state *agentcontract.RuntimeState, limit int, capabilities map[string]agentcontract.CapabilityDescription) bool {
	if state.Progress == nil {
		state.Progress = &agentcontract.ProgressTracker{}
	}

	// Deduplicate content: new call refs or Fact IDs alone are not progress.
	keys := map[string]bool{}
	readKeys := browserReadProgressKeys(state, capabilities)
	for _, fact := range state.Facts {
		key, ok := readKeys[fact.FactID]
		if !ok {
			key = progressKey(agentcontract.JSON{"capability": fact.SourceCapability, "value": fact.Value})
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

// RunProgress projects bounded task progress from retained invocation and fact evidence.
func RunProgress(state *agentcontract.RuntimeState, limit int) *agentcontract.RunProgress {
	if len(state.Observations) == 0 && len(state.Pending) == 0 && len(state.Decisions) == 0 && (state.Progress == nil || state.Progress.NoProgressRounds == 0) {
		return nil
	}
	view := &agentcontract.RunProgress{}
	if state.Progress != nil {
		view.NoProgressRounds = state.Progress.NoProgressRounds
		view.StagnationWarning = view.NoProgressRounds >= max(2, limit-2)
	}
	active := map[string]bool{}
	for _, item := range state.Pending {
		if item.Status != "succeeded" && item.Status != "failed" {
			active[item.Call.CallRef] = true
			view.Pending = append(view.Pending, agentcontract.ProgressItem{Capability: item.Call.Capability, CallRef: item.Call.CallRef, Status: item.Status, ErrorCode: item.ErrorCode})
		}
	}
	observations := currentEvidenceObservations(state, state.Observations)
	latest := map[string]int{}
	for i, obs := range observations {
		latest[obs.CallRef] = i
	}

	// Keep the last successful result of each capability so a busy recent tool
	// does not crowd earlier work out of the observation window.
	completed := map[string]agentcontract.ProgressItem{}
	blocked := map[string]agentcontract.ProgressItem{}
	for i, obs := range observations {
		if latest[obs.CallRef] != i || active[obs.CallRef] {
			continue
		}
		item := agentcontract.ProgressItem{Capability: obs.Capability, CallRef: obs.CallRef, Status: obs.Status, FactID: obs.FactID, ErrorCode: obs.ErrorCode}
		if obs.Status == "succeeded" {
			view.CompletedCount++
			completed[obs.Capability] = item
			delete(blocked, obs.Capability)
		} else {
			view.BlockedCount++
			blocked[obs.Capability] = item
		}
	}
	project := func(items map[string]agentcontract.ProgressItem) []agentcontract.ProgressItem {
		out := make([]agentcontract.ProgressItem, 0, len(items))
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
func currentEvidenceObservations(state *agentcontract.RuntimeState, observations []agentcontract.Observation) []agentcontract.Observation {
	facts := map[string]agentcontract.Fact{}
	for _, fact := range state.Facts {
		facts[fact.FactID] = fact
	}
	polls := map[string]agentcontract.Observation{}
	for _, observation := range state.Observations {
		if observation.FactID == nil || observation.ErrorCode != nil || !strings.HasPrefix(observation.CallRef, "poll-") {
			continue
		}
		if _, retained := facts[*observation.FactID]; !retained {
			continue
		}
		end := strings.LastIndex(observation.CallRef, "-")
		if end > len("poll-") {
			polls[observation.CallRef[len("poll-"):end]] = observation
		}
	}
	// Reconciliation appends a newer outcome for the original call reference.
	// Present one current outcome per call without changing the saved history.
	latest := map[string]int{}
	for i, observation := range observations {
		if observation.CallRef != "" {
			latest[observation.CallRef] = i
		}
	}
	result := make([]agentcontract.Observation, 0, len(observations))
	for i, observation := range observations {
		if observation.CallRef == "" || latest[observation.CallRef] == i {
			result = append(result, observation)
		}
	}
	redundantPolls := map[string]bool{}
	for i, observation := range result {
		if observation.FactID == nil || observation.ErrorCode != nil {
			continue
		}
		id := DeterministicInvocationID(state.RunID, observation.CallRef)
		if poll, ok := polls[id]; ok {
			if fact, retained := facts[*observation.FactID]; retained && *observation.FactID != *poll.FactID {
				// A retained original result must not borrow a different Fact. A
				// verified receipt for this same command supersedes its old poll.
				commandID, err := agentcontract.ValueAt(fact.Value, []any{"data", "command_id"})
				if err == nil && commandID == id && fact.SourceCapability == poll.Capability && fact.Quality == "verified_reconciliation" {
					redundantPolls[poll.CallRef] = true
				}
				continue
			}
			result[i].FactID = poll.FactID
			redundantPolls[poll.CallRef] = true
		}
	}

	// The original action now carries this invocation's current result. Keep a
	// standalone poll when its action is absent, but do not spend two observation
	// slots or count two completions for the same action. Audit state is unchanged.
	projected := make([]agentcontract.Observation, 0, len(result))
	for _, observation := range result {
		if !redundantPolls[observation.CallRef] {
			projected = append(projected, observation)
		}
	}
	return projected
}

const progressUsagePrompt = "progress summarizes observed outcomes; pending is unfinished. Reuse cited Facts. On stagnation_warning, change approach or explain the verified limitation."

// Give a concrete action/observation correction before the hard stop, rather
// than relying only on the late, generic stagnation flag. Compare full retained
// evidence; equal previews alone cannot establish that a result is unchanged.
func (r *AgentRuntime) repeatedReadPrompt(state *agentcontract.RuntimeState, packet agentcontract.ContextPacket) string {
	if packet.Progress == nil || packet.Progress.NoProgressRounds == 0 || len(packet.Progress.Pending) > 0 {
		return ""
	}
	// Supplied input does not always have a saved observation boundary. Do not
	// classify reads preceding that input as repetitions of its requested work.
	if state.InputField != nil || len(state.Followups) > 0 || state.SteeringCursor > 0 {
		return ""
	}
	observations := currentEvidenceObservations(state, state.Observations)
	if len(observations) < 2 {
		return ""
	}
	last := observations[len(observations)-1]
	caps := r.Provider.Capabilities()
	cap, ok := caps[last.Capability]
	if !ok || !r.Grants[last.Capability] || cap.Effect != "read" || last.FactID == nil {
		return ""
	}
	visible := false
	for _, fact := range packet.Facts {
		visible = visible || (fact.FactID == *last.FactID && fact.ReferenceAvailable)
	}
	if !visible {
		return ""
	}
	facts := map[string]agentcontract.Fact{}
	for _, fact := range state.Facts {
		facts[fact.FactID] = fact
	}
	readKeys := browserReadProgressKeys(state, caps)
	key := func(observation agentcontract.Observation) string {
		if observation.Status != "succeeded" || observation.ErrorCode != nil || observation.ArgumentsOmitted || observation.FactID == nil {
			return ""
		}
		fact, ok := facts[*observation.FactID]
		if !ok {
			return ""
		}
		result, ok := readKeys[fact.FactID]
		if !ok {
			result = progressKey(agentcontract.JSON{"capability": fact.SourceCapability, "value": fact.Value})
		}
		if result == "" {
			return ""
		}
		return progressKey(agentcontract.JSON{"capability": observation.Capability, "arguments": observation.Arguments, "result": result})
	}
	latest := key(last)
	if latest == "" {
		return ""
	}
	count := 0
	seen := map[string]bool{}
	for i := len(observations) - 1; i >= 0; i-- {
		observation := observations[i]
		if key(observation) != latest {
			break
		}
		if !seen[observation.CallRef] {
			seen[observation.CallRef] = true
			count++
		}
	}
	if count < 2 {
		return ""
	}
	return fmt.Sprintf("Repeated completed read detected (runtime metadata, not a new task or permission): capability %q has succeeded %d consecutive times with the same arguments and unchanged result. The latest read is already complete; a new decision round does not require repeating a completed prerequisite. Use its available Fact to continue the next unfinished authorized action or inspect that action's contract. Do not restart completed work. Read again only if the task explicitly needs another sample, a subsequent action or new input requires verification, or actual changed state or a context rejection requires a refresh. This feedback does not replace an explicitly requested read after a write, authorize any operation, or claim the task is complete.", last.Capability, count)
}
