package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

// Steer appends authorized user input under a stable request identity and expected run revision.
func (h *AgentHost) Steer(ctx context.Context, id, owner, requestID, text string, revision int) (agentcontract.StoredRun, error) {
	if _, err := h.Get(ctx, id, owner); err != nil {
		return agentcontract.StoredRun{}, err
	}
	if !agentcontract.ValidUUID(requestID) || revision < 0 || strings.TrimSpace(text) == "" || len([]rune(text)) > 30000 {
		return agentcontract.StoredRun{}, agentcontract.NewHostError("steering_invalid")
	}
	return h.Store.QueueSteering(id, owner, requestID, text, revision)
}

func (h *AgentHost) applySteering(run agentcontract.StoredRun, state *agentcontract.RuntimeState) (agentcontract.StoredRun, bool, error) {
	for _, item := range state.Pending {
		if item.Status == "in_flight" || item.Status == "unknown" || item.PollInFlight {
			return run, false, nil
		}
	}
	messages, err := h.Store.PendingSteering(run.RunID, run.OwnerID, state.SteeringCursor)
	if err != nil || len(messages) == 0 {
		return run, false, err
	}
	var inputs []runstore.MemoryInput
	if run.State["memory_inputs"] != nil {
		raw, err := agentcontract.CanonicalJSON(run.State["memory_inputs"])
		if err != nil {
			return run, false, agentcontract.NewHostError("run_state_invalid")
		}
		if err = jsonvalue.DecodeStrict(raw, &inputs); err != nil {
			return run, false, agentcontract.NewHostError("run_state_invalid")
		}
	}
	for _, message := range messages {
		state.Followups = append(state.Followups, "steering: "+message.Text)
		state.SteeringCursor = message.Sequence
		inputs = append(inputs, runstore.MemoryInput{ID: fmt.Sprintf("%s:steering:%s", run.RunID, message.RequestID), Text: message.Text})
	}
	run.State["memory_inputs"] = inputs

	// Only unsent calls are superseded. Settled evidence and submitted jobs remain.
	for i := range state.Pending {
		item := &state.Pending[i]
		if item.Attempts == 0 && (item.Status == "prepared" || item.Status == "needs_approval") {
			reactcore.Observe(state, item, agentcontract.CallOutcome{ErrorCode: "steering_superseded"})
			item.ApprovedHash, item.ApprovedUntil = nil, nil
		}
	}
	state.Status = "running"
	state.AnswerMarkdown = ""
	state.InputField, state.InputPrompt, state.ErrorCode = nil, nil, nil
	run, err = h.save(run, state, "", nil, agentcontract.JSON{"kind": "steering_applied", "through_sequence": state.SteeringCursor, "count": len(messages)})
	return run, true, err
}
