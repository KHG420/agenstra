package deployment

import (
	"context"
	"errors"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
)

func scheduleRequest(spec agentcontract.ScheduleSpec) agentcontract.ScheduleRequest {
	return agentcontract.ScheduleRequest{Name: "Daily report", PackID: "records", Instruction: "Look up R-1", Schedule: spec}
}

func createSchedule(t *testing.T, h *durablehost.AgentHost, spec agentcontract.ScheduleSpec) agentcontract.ScheduledTask {
	t.Helper()
	task, err := h.CreateSchedule(t.Context(), "alice", scheduleRequest(spec))
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func scheduleHost(t *testing.T, now *float64) *durablehost.AgentHost {
	t.Helper()
	s := testStore(t)
	s.Clock = func() float64 { return *now }
	h := testHost(t, s, &hostProvider{}, &hostModel{})
	h.Clock = s.Clock
	return h
}

func TestScheduleAuthorizationRecheckAndTransientRetry(t *testing.T) {
	now, at := 1000., 1010.
	h := scheduleHost(t, &now)
	task := createSchedule(t, h, agentcontract.ScheduleSpec{Kind: "once", At: &at})
	now = 1010.
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{}, errors.New("private backend outage details")
	}
	if _, err := h.DispatchDueSchedules(t.Context(), 100); agentcontract.ErrorCode(err) != "authorization_unavailable" {
		t.Fatal(err)
	}
	var callErr8 error
	task, callErr8 = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if callErr8 != nil {
		t.Error(callErr8)
	}
	if task.Status != "active" || task.LastExecution != nil || *task.NextRunAt != 1010 {
		t.Fatalf("transient failure consumed schedule: %+v", task)
	}
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{}, DeploymentError("access_denied")
	}
	if n, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("denial: %d %v", n, err)
	}
	var callErr9 error
	task, callErr9 = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if callErr9 != nil {
		t.Error(callErr9)
	}
	if task.Status != "paused" || task.LastExecution.Status != "needs_authorization" || task.LastExecution.ErrorCode != "access_denied" {
		t.Fatalf("denial state: %+v", task)
	}
	if _, err := h.ResumeSchedule(t.Context(), task.ScheduleID, "alice", task.Revision); agentcontract.ErrorCode(err) != "access_denied" {
		t.Fatal(err)
	}
	runs, callErr10 := h.Store.ListRuns("alice", 100)
	if callErr10 != nil {
		t.Error(callErr10)
	}
	if len(runs) != 0 {
		t.Fatal("unauthorized run created")
	}
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{AllowModelData: true}, nil
	}
	task, err := h.ResumeSchedule(t.Context(), task.ScheduleID, "alice", task.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("overdue once resume: %d %v", n, err)
	}
}
