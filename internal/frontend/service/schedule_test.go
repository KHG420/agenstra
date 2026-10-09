package service

import (
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
