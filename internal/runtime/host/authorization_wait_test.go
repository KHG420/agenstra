package host

import (
	"context"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func TestUnavailableAuthorizationWaitsBeforeDispatchAndRecovers(t *testing.T) {
	provider := &hostProvider{}
	model := &hostModel{decisions: []agentcontract.Decision{callDecision("records.get")}}
	h := testHost(t, testStore(t), provider, model)
	now := runstore.UnixNow()
	h.Clock = func() float64 { return now }
	run := createTestHostRun(t, h)
	original := h.PolicyResolver
	unavailable := true
	h.PolicyResolver = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
		if unavailable {
			return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("authorization_unavailable")
		}
		return original(ctx, owner, pack)
	}
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" || run.NextWakeAt == nil || *run.NextWakeAt <= now || model.calls != 0 || provider.calls != 0 {
		t.Fatalf("temporary authorization failure blocked or dispatched work: status=%s err=%v", run.Status, err)
	}
	now = *run.NextWakeAt
	unavailable = false
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || provider.calls != 1 {
		t.Fatalf("current authorization did not resume the existing run: status=%s calls=%d err=%v", run.Status, provider.calls, err)
	}
}

func TestAuthorizationWaitKeepsDeadlineCancellationAndDenial(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel", "denial"} {
		t.Run(mode, func(t *testing.T) {
			provider := &hostProvider{}
			model := &hostModel{}
			h := testHost(t, testStore(t), provider, model)
			h.Settings.MaxRunSeconds = 5
			now := runstore.UnixNow()
			h.Clock = func() float64 { return now }
			run := createTestHostRun(t, h)
			original := h.PolicyResolver
			h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
				return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("authorization_unavailable")
			}
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "waiting" || run.NextWakeAt == nil {
				t.Fatal(run.Status, err)
			}
			want := "failed"
			switch mode {
			case "deadline":
				now = run.CreatedAt + 5
			case "cancel":
				h.PolicyResolver = original
				if _, err := h.Cancel(t.Context(), run.RunID, "alice"); err != nil {
					t.Fatal(err)
				}
				want = "cancelled"
			case "denial":
				now = *run.NextWakeAt
				h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
					return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("access_denied")
				}
				want = "needs_authorization"
			}
			run, err = h.Drive(t.Context(), run.RunID, "alice")
			if run.Status != want || run.NextWakeAt != nil || model.calls != 0 || provider.calls != 0 {
				t.Fatalf("authorization waiting bypassed %s: status=%s err=%v", mode, run.Status, err)
			}
			if mode != "denial" && err != nil {
				t.Fatal(err)
			}
			state, restoreErr := h.Restore(run)
			if restoreErr != nil || mode == "deadline" && (state.ErrorCode == nil || *state.ErrorCode != "run_deadline_exceeded") {
				t.Fatal(state, restoreErr)
			}
		})
	}
}

func TestWorkflowAuthorizationWaitPreservesCompletedSpecialist(t *testing.T) {
	h, provider, parent, workflow, child := queuedSpecialist(t)
	child, err := h.Drive(t.Context(), child.RunID, "alice")
	if err != nil || child.Status != "completed" || provider.calls != 1 {
		t.Fatal(child.Status, err)
	}
	childRevision := child.Revision
	now := max(*parent.NextWakeAt, *workflow.NextWakeAt) + 1
	h.Clock = func() float64 { return now }
	original := h.PolicyResolver
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("authorization_unavailable")
	}
	workflow, err = h.Drive(t.Context(), workflow.RunID, "alice")
	if err != nil || workflow.Status != "waiting" || workflow.NextWakeAt == nil {
		t.Fatal(workflow.Status, err)
	}
	now = *workflow.NextWakeAt
	h.PolicyResolver = original
	workflow, err = h.Drive(t.Context(), workflow.RunID, "alice")
	if err != nil || workflow.Status != "completed" {
		t.Fatal(workflow.Status, err)
	}
	parent, err = h.Drive(t.Context(), parent.RunID, "alice")
	if err != nil || parent.Status != "completed" || provider.calls != 1 {
		t.Fatal("specialist business call was repeated", parent.Status, provider.calls, err)
	}
	child, err = h.Store.GetRun(child.RunID, "alice")
	if err != nil || child.Revision != childRevision {
		t.Fatal("completed specialist was restarted", child.Revision, err)
	}
}

func TestOperationPollAuthorizationWaitKeepsTheExistingReceipt(t *testing.T) {
	h, provider, parent, _, _ := queuedSpecialist(t)
	now := *parent.NextWakeAt + 1
	h.Clock = func() float64 { return now }
	parent, err := h.Store.Claim(parent.RunID, "alice", h.Settings.LeaseSeconds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Store.Release(parent.RunID, "alice", parent.LeaseToken); err != nil {
			t.Error(err)
		}
	})
	state, err := h.Restore(parent)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := h.OpenRunProvider(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	item := &state.Pending[0]
	invocationID, operationID, polls := item.InvocationID, item.Operation.OperationID, state.PollCallsUsed
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("authorization_unavailable")
	}
	parent, err = h.poll(t.Context(), parent, state, item, connection, &reactcore.AgentRuntime{ConnectionID: agentcontract.NewID(), Durable: true})
	if err != nil || parent.Status != "waiting" || parent.NextWakeAt == nil || provider.calls != 0 {
		t.Fatal(parent.Status, provider.calls, err)
	}
	if item.InvocationID != invocationID || item.Operation.OperationID != operationID || item.Status != "waiting" || item.PollInFlight || state.PollCallsUsed != polls {
		t.Fatal("authorization retry changed a submitted operation", item)
	}
}
