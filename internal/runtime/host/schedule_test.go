package host

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func scheduleRequest(spec agentcontract.ScheduleSpec) agentcontract.ScheduleRequest {
	return agentcontract.ScheduleRequest{Name: "Daily report", PackID: "records", Instruction: "Look up R-1", Schedule: spec}
}

func createSchedule(t *testing.T, h *AgentHost, spec agentcontract.ScheduleSpec) agentcontract.ScheduledTask {
	t.Helper()
	task, err := h.CreateSchedule(t.Context(), "alice", scheduleRequest(spec))
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func scheduleHost(t *testing.T, now *float64) *AgentHost {
	t.Helper()
	s := testStore(t)
	s.Clock = func() float64 { return *now }
	h := testHost(t, s, &hostProvider{}, &hostModel{})
	h.Clock = s.Clock
	return h
}

func TestScheduleValidation(t *testing.T) {
	now := 1000.
	h := scheduleHost(t, &now)
	past, future := 999., 1100.
	for _, spec := range []agentcontract.ScheduleSpec{
		{Kind: "unknown"}, {Kind: "once"}, {Kind: "once", At: &past}, {Kind: "once", At: &future, Cron: "* * * * *"},
		{Kind: "interval"}, {Kind: "interval", IntervalSeconds: -1}, {Kind: "interval", IntervalSeconds: 31536001},
		{Kind: "cron", Cron: "* * * * * *"}, {Kind: "cron", Cron: "61 * * * *"}, {Kind: "cron", Cron: "*/0 * * * *"},
		{Kind: "cron", Cron: "0 9 * * *", Timezone: "Local"}, {Kind: "cron", Cron: "0 9 * * *", Timezone: "Mars/City"},
		{Kind: "cron", Cron: "0 0 31 2 *"}, {Kind: "cron", Cron: "0 0 * * MON"},
	} {
		if _, err := h.CreateSchedule(t.Context(), "alice", scheduleRequest(spec)); agentcontract.ErrorCode(err) != "schedule_invalid" {
			t.Errorf("spec %+v: %v", spec, err)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), 253402300800} {
		if _, err := h.CreateSchedule(t.Context(), "alice", scheduleRequest(agentcontract.ScheduleSpec{Kind: "once", At: &value})); agentcontract.ErrorCode(err) != "schedule_invalid" {
			t.Errorf("at %v: %v", value, err)
		}
	}
	request := scheduleRequest(agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 60})
	request.Instruction = " "
	if _, err := h.CreateSchedule(t.Context(), "alice", request); agentcontract.ErrorCode(err) != "schedule_invalid" {
		t.Fatal(err)
	}
}

func TestScheduleOnceDispatchAndResult(t *testing.T) {
	now, at := 1000., 1100.
	h := scheduleHost(t, &now)
	task := createSchedule(t, h, agentcontract.ScheduleSpec{Kind: "once", At: &at})

	// A caller cannot mutate the persisted definition through its input pointer.
	at = 999.
	if count, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || count != 0 {
		t.Fatalf("early: %d %v", count, err)
	}
	now = 1100.
	if count, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || count != 1 {
		t.Fatalf("due: %d %v", count, err)
	}
	if count, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || count != 0 {
		t.Fatalf("repeat: %d %v", count, err)
	}
	task, err := h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if err != nil || task.Status != "completed" || task.NextRunAt != nil || task.LastExecution == nil || task.LastExecution.Status != "queued" {
		t.Fatalf("dispatched: %+v %v", task, err)
	}
	if _, err = h.ResumeSchedule(t.Context(), task.ScheduleID, "alice", task.Revision); agentcontract.ErrorCode(err) != "schedule_completed" {
		t.Fatalf("completed resume: %v", err)
	}
	if _, err = h.WakeDue(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	task, err = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if err != nil || task.LastExecution.Status != "completed" {
		t.Fatalf("result: %+v %v", task, err)
	}
	run, err := h.Get(t.Context(), task.LastExecution.RunID, "alice")
	state, callErr2 := h.Restore(run)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if err != nil || state == nil || state.AnswerMarkdown != "Done" {
		t.Fatalf("run result: %+v %v", state, err)
	}
	events, err := h.Store.ListEvents(run.RunID, "alice", 0, 10)
	if err != nil || len(events) == 0 {
		t.Fatalf("events: %v %v", events, err)
	}
	event, ok := events[0]["event"].(map[string]any)
	if !ok || event["kind"] != "schedule_triggered" || event["schedule_id"] != task.ScheduleID {
		t.Fatalf("trigger event: %v", events[0])
	}
}

func TestScheduleMissedIntervalOverlapAndHistory(t *testing.T) {
	now := 1000.
	h := scheduleHost(t, &now)
	task := createSchedule(t, h, agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 60})
	now = 1245.
	if n, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("catchup: %d %v", n, err)
	}
	var callErr3 error
	task, callErr3 = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if *task.NextRunAt != 1300 || task.LastExecution.ScheduledAt != 1060 {
		t.Fatalf("cadence: %+v", task)
	}
	now = 1300.
	if _, err := h.DispatchDueSchedules(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	var callErr4 error
	task, callErr4 = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if callErr4 != nil {
		t.Error(callErr4)
	}
	if task.LastExecution.Status != "skipped_overlap" || *task.NextRunAt != 1360 {
		t.Fatalf("overlap: %+v", task)
	}
	runs, callErr5 := h.Store.ListRuns("alice", 100)
	if callErr5 != nil {
		t.Error(callErr5)
	}
	if len(runs) != 1 {
		t.Fatalf("overlapping runs: %d", len(runs))
	}
	if _, err := h.WakeDue(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	now = 1360.
	if _, err := h.DispatchDueSchedules(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	history, err := h.ListScheduleExecutions(t.Context(), task.ScheduleID, "alice", 0, 100)
	if err != nil || len(history) != 3 || history[0].Status != "completed" || history[1].Status != "skipped_overlap" || history[2].Status != "queued" {
		t.Fatalf("history: %+v %v", history, err)
	}
	page, err := h.ListScheduleExecutions(t.Context(), task.ScheduleID, "alice", history[0].Sequence, 1)
	if err != nil || len(page) != 1 || page[0].Sequence != history[1].Sequence {
		t.Fatalf("page: %+v %v", page, err)
	}
}

func TestScheduleOwnerRevisionPauseUpdateDelete(t *testing.T) {
	now := 1000.
	h := scheduleHost(t, &now)
	task := createSchedule(t, h, agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 60})
	if _, err := h.GetSchedule(t.Context(), task.ScheduleID, "bob"); agentcontract.ErrorCode(err) != "not_found" {
		t.Fatal(err)
	}
	if _, err := h.ListScheduleExecutions(t.Context(), task.ScheduleID, "bob", 0, 100); agentcontract.ErrorCode(err) != "not_found" {
		t.Fatal(err)
	}
	if err := h.DeleteSchedule(t.Context(), task.ScheduleID, "bob", 0); agentcontract.ErrorCode(err) != "not_found" {
		t.Fatal(err)
	}
	list, err := h.ListSchedules(t.Context(), "bob", 100)
	if err != nil || len(list) != 0 {
		t.Fatalf("owner list: %+v %v", list, err)
	}
	if _, err = h.PauseSchedule(t.Context(), task.ScheduleID, "alice", 99); agentcontract.ErrorCode(err) != "revision_conflict" {
		t.Fatal(err)
	}
	task, err = h.PauseSchedule(t.Context(), task.ScheduleID, "alice", task.Revision)
	if err != nil {
		t.Fatal(err)
	}
	now = 1100.
	if n, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("paused dispatch: %d %v", n, err)
	}
	task, err = h.UpdateSchedule(t.Context(), task.ScheduleID, "alice", task.Revision, scheduleRequest(agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 120}))
	if err != nil || task.Status != "paused" || *task.NextRunAt != 1220 {
		t.Fatalf("paused update: %+v %v", task, err)
	}
	now = 1500.
	task, err = h.ResumeSchedule(t.Context(), task.ScheduleID, "alice", task.Revision)
	if err != nil || task.Status != "active" || *task.NextRunAt != 1580 {
		t.Fatalf("resume cadence: %+v %v", task, err)
	}
	now = 1580.
	if _, err = h.DispatchDueSchedules(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	var callErr6 error
	task, callErr6 = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if callErr6 != nil {
		t.Error(callErr6)
	}
	if err = h.DeleteSchedule(t.Context(), task.ScheduleID, "alice", task.Revision-1); agentcontract.ErrorCode(err) != "revision_conflict" {
		t.Fatal(err)
	}
	if err = h.DeleteSchedule(t.Context(), task.ScheduleID, "alice", task.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = h.GetSchedule(t.Context(), task.ScheduleID, "alice"); agentcontract.ErrorCode(err) != "not_found" {
		t.Fatal(err)
	}
	runs, callErr7 := h.Store.ListRuns("alice", 100)
	if callErr7 != nil {
		t.Error(callErr7)
	}
	if len(runs) != 1 {
		t.Fatal("delete removed a dispatched run")
	}
}

func TestScheduleReopenAndCompetingDispatch(t *testing.T) {
	now := 1000.
	h := scheduleHost(t, &now)
	task := createSchedule(t, h, agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 60})
	secondStore, err := runstore.NewSQLiteStore(h.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(secondStore.Close)
	if err = secondStore.Initialize(); err != nil {
		t.Fatal(err)
	}
	secondStore.Clock = h.Store.Clock
	second := testHost(t, secondStore, &hostProvider{}, &hostModel{})
	second.Clock = h.Clock
	now = 1100.
	var wg sync.WaitGroup
	for i := range 8 {
		host := h
		if i%2 == 0 {
			host = second
		}
		wg.Go(func() {
			if _, err := host.DispatchDueSchedules(t.Context(), 100); err != nil {
				t.Errorf("concurrent dispatch: %v", err)
			}
		})
	}
	wg.Wait()
	runs, err := h.Store.ListRuns("alice", 100)
	if err != nil || len(runs) != 1 {
		t.Fatalf("duplicate runs: %d %v", len(runs), err)
	}
	history, err := second.ListScheduleExecutions(t.Context(), task.ScheduleID, "alice", 0, 100)
	if err != nil || len(history) != 1 {
		t.Fatalf("duplicate history: %+v %v", history, err)
	}
	var version int
	if err = secondStore.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("v1 schema changed: %d %v", version, err)
	}
}

func TestSchedulePauseDuringAuthorizationFencesDispatch(t *testing.T) {
	now := 1000.
	h := scheduleHost(t, &now)
	task := createSchedule(t, h, agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 60})
	now = 1060.
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		_, err := h.PauseSchedule(t.Context(), task.ScheduleID, "alice", task.Revision)
		return agentcontract.ExecutionPolicy{AllowModelData: true}, err
	}
	if n, err := h.DispatchDueSchedules(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("stale dispatch: %d %v", n, err)
	}
	runs, callErr11 := h.Store.ListRuns("alice", 100)
	if callErr11 != nil {
		t.Error(callErr11)
	}
	if len(runs) != 0 {
		t.Fatal("stale definition dispatched")
	}
}

func TestScheduleDispatchRollbackAndCancellation(t *testing.T) {
	now := 1000.
	h := scheduleHost(t, &now)
	task := createSchedule(t, h, agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 60})
	now = 1060.
	if _, err := h.Store.DB.Exec(`CREATE TRIGGER reject_schedule_history BEFORE INSERT ON schedule_executions BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DispatchDueSchedules(t.Context(), 100); err == nil {
		t.Fatal("expected transaction failure")
	}
	runs, callErr12 := h.Store.ListRuns("alice", 100)
	if callErr12 != nil {
		t.Error(callErr12)
	}
	var callErr13 error
	task, callErr13 = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if callErr13 != nil {
		t.Error(callErr13)
	}
	if len(runs) != 0 || task.Revision != 0 || *task.NextRunAt != 1060 || task.LastExecution != nil {
		t.Fatalf("partial dispatch committed: %+v %v", task, runs)
	}
	if _, err := h.Store.DB.Exec("DROP TRIGGER reject_schedule_history"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		cancel()
		return agentcontract.ExecutionPolicy{AllowModelData: true}, nil
	}
	if _, err := h.DispatchDueSchedules(ctx, 100); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var callErr14 error
	runs, callErr14 = h.Store.ListRuns("alice", 100)
	if callErr14 != nil {
		t.Error(callErr14)
	}
	if len(runs) != 0 {
		t.Fatal("canceled dispatch committed")
	}
}

func TestScheduleUsesTriggerReleaseAndPreservesApproval(t *testing.T) {
	now := 1000.
	h := scheduleHost(t, &now)
	release := "release-before"
	h.ReleaseResolver = func(context.Context, string, string) (string, error) { return release, nil }
	task := createSchedule(t, h, agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 60})
	release = "release-at-trigger"
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {Name: "records.get", Version: "1", Description: "Compute", InputSchema: agentcontract.JSON{"type": "object"}, Effect: "compute", Replay: "never", ReferenceScope: "durable", ApprovalRequired: true}}}
	h.ProviderFactory = func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return p, nil }
	h.Model = &hostModel{decisions: []agentcontract.Decision{callDecision("records.get")}}
	now = 1060.
	if _, err := h.DispatchDueSchedules(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	var callErr15 error
	task, callErr15 = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if callErr15 != nil {
		t.Error(callErr15)
	}
	run, err := h.Get(t.Context(), task.LastExecution.RunID, "alice")
	if err != nil || run.State["pack_release"] != "release-at-trigger" {
		t.Fatalf("release: %+v %v", run, err)
	}
	if _, err = h.WakeDue(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	var callErr16 error
	task, callErr16 = h.GetSchedule(t.Context(), task.ScheduleID, "alice")
	if callErr16 != nil {
		t.Error(callErr16)
	}
	if task.LastExecution.Status != "needs_approval" || p.calls != 0 {
		t.Fatalf("approval bypassed: %+v calls=%d", task, p.calls)
	}
	now = 1120.
	if _, err = h.DispatchDueSchedules(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	history, callErr17 := h.ListScheduleExecutions(t.Context(), task.ScheduleID, "alice", 0, 100)
	if callErr17 != nil {
		t.Error(callErr17)
	}
	if len(history) != 2 || history[1].Status != "skipped_overlap" {
		t.Fatalf("approval overlap: %+v", history)
	}
	var callErr18 error
	run, callErr18 = h.Get(t.Context(), run.RunID, "alice")
	if callErr18 != nil {
		t.Error(callErr18)
	}
	state, callErr19 := h.Restore(run)
	if callErr19 != nil {
		t.Error(callErr19)
	}
	item := state.Pending[0]
	if _, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err = h.WakeDue(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	var callErr20 error
	history, callErr20 = h.ListScheduleExecutions(t.Context(), task.ScheduleID, "alice", 0, 100)
	if callErr20 != nil {
		t.Error(callErr20)
	}
	if history[0].Status != "completed" || p.calls != 1 {
		t.Fatalf("approved result: %+v calls=%d", history, p.calls)
	}
}

func TestScheduleTablesAddedToExistingV1Store(t *testing.T) {
	s := testStore(t)
	run := testRun(t, s, "existing-run")
	if _, err := s.DB.Exec("DROP TABLE schedule_executions; DROP TABLE scheduled_tasks;"); err != nil {
		t.Fatal(err)
	}
	if err := s.Initialize(); err != nil {
		t.Fatal(err)
	}
	now := 1000.
	h := testHost(t, s, &hostProvider{}, &hostModel{})
	h.Clock = func() float64 { return now }
	createSchedule(t, h, agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 60})
	got, err := s.GetRun(run.RunID, "alice")
	if err != nil || got.State["value"] != "original" || got.Revision != run.Revision {
		t.Fatalf("existing v1 run: %+v %v", got, err)
	}
}
