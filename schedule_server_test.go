package agenstra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scheduleServer(t *testing.T, worker bool) (*HTTPServer, *AgentHost) {
	t.Helper()
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	d := &Deployment{Config: DeploymentConfig{Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY"}, "bob": {APIKeyEnv: "BOB_KEY"}}}, Environment: map[string]string{"ALICE_KEY": "alice-secret", "BOB_KEY": "bob-secret"}}
	s, err := NewHTTPServer(h, d, worker, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, h
}
func scheduleHTTP(t *testing.T, s *HTTPServer, method, path, owner, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if owner != "" {
		req.Header.Set("Authorization", "Bearer "+owner+"-secret")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}
func decodeSchedule(t *testing.T, w *httptest.ResponseRecorder) ScheduledTask {
	t.Helper()
	var task ScheduledTask
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestScheduleHTTPManagementAuthAndResults(t *testing.T) {
	s, h := scheduleServer(t, false)
	now := 1000.
	h.Clock = func() float64 { return now }
	h.Store.Clock = h.Clock
	body := `{"name":"Daily report","pack_id":"records","instruction":"Look up R-1","schedule":{"kind":"interval","interval_seconds":60}}`
	scheduleHTTP(t, s, "POST", "/schedules", "", body, 401)
	task := decodeSchedule(t, scheduleHTTP(t, s, "POST", "/schedules", "alice", body, 201))
	path := "/schedules/" + task.ScheduleID
	for _, tc := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"PUT", "", body[:len(body)-1] + `,"revision":0}`},
		{"POST", "/pause", `{"revision":0}`}, {"POST", "/resume", `{"revision":0}`},
		{"DELETE", "?revision=0", ""}, {"GET", "/executions", ""},
	} {
		scheduleHTTP(t, s, tc.method, path+tc.suffix, "bob", tc.body, 404)
	}
	scheduleHTTP(t, s, "GET", "/schedules", "bob", "", 200)
	listed := scheduleHTTP(t, s, "GET", "/schedules", "alice", "", 200)
	if !strings.Contains(listed.Body.String(), task.ScheduleID) {
		t.Fatal("created task missing from list")
	}
	scheduleHTTP(t, s, "POST", path+"/pause", "alice", `{"revision":9}`, 409)
	task = decodeSchedule(t, scheduleHTTP(t, s, "POST", path+"/pause", "alice", `{"revision":0}`, 200))
	updated := strings.Replace(body, "60", "120", 1)
	task = decodeSchedule(t, scheduleHTTP(t, s, "PUT", path, "alice", updated[:len(updated)-1]+fmt.Sprintf(`,"revision":%d}`, task.Revision), 200))
	if task.Status != "paused" || task.Schedule.IntervalSeconds != 120 {
		t.Fatalf("update: %+v", task)
	}
	task = decodeSchedule(t, scheduleHTTP(t, s, "POST", path+"/resume", "alice", fmt.Sprintf(`{"revision":%d}`, task.Revision), 200))
	now = *task.NextRunAt
	if _, err := h.DispatchDueSchedules(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := h.WakeDue(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	task = decodeSchedule(t, scheduleHTTP(t, s, "GET", path, "alice", "", 200))
	if task.LastExecution == nil || task.LastExecution.Status != "completed" {
		t.Fatalf("current result: %+v", task)
	}
	history := scheduleHTTP(t, s, "GET", path+"/executions", "alice", "", 200)
	if !strings.Contains(history.Body.String(), task.LastExecution.RunID) || strings.Contains(history.Body.String(), "lease_token") {
		t.Fatalf("history: %s", history.Body.String())
	}
	result := scheduleHTTP(t, s, "GET", "/runs/"+task.LastExecution.RunID, "alice", "", 200)
	if !strings.Contains(result.Body.String(), `"answer_markdown":"Done"`) {
		t.Fatalf("run result: %s", result.Body.String())
	}
	scheduleHTTP(t, s, "DELETE", path+fmt.Sprintf("?revision=%d", task.Revision), "alice", "", 204)
	scheduleHTTP(t, s, "GET", path, "alice", "", 404)
	scheduleHTTP(t, s, "GET", "/runs/"+task.LastExecution.RunID, "alice", "", 200)
}

func TestScheduleHTTPInvalidRequestsAndGrantDenial(t *testing.T) {
	s, h := scheduleServer(t, false)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/schedules", `{}`},
		{"POST", "/schedules", `{"name":"x","pack_id":"records","instruction":"do it","schedule":{"kind":"cron","cron":"0 0 31 2 *"}}`},
		{"POST", "/schedules", `{"name":"x","pack_id":"records","instruction":"do it","schedule":{"kind":"interval","interval_seconds":10},"owner_id":"bob"}`},
		{"POST", "/schedules", `{"name":"x","pack_id":"records","instruction":"do it","schedule":{"kind":"interval","interval_seconds":10,"unexpected":true}}`},
		{"POST", "/schedules", `{} {}`}, {"POST", "/schedules", `null`},
		{"GET", "/schedules?limit=0", ""}, {"GET", "/schedules?limit=1001", ""}, {"GET", "/schedules?limit=no", ""},
		{"PUT", "/schedules/missing", `{}`}, {"POST", "/schedules/missing/pause", `{}`},
		{"POST", "/schedules/missing/resume", `{"revision":null}`}, {"DELETE", "/schedules/missing", ""},
		{"DELETE", "/schedules/missing?revision=-1", ""},
	} {
		scheduleHTTP(t, s, tc.method, tc.path, "alice", tc.body, 422)
	}
	body := `{"name":"x","pack_id":"records","instruction":"do it","schedule":{"kind":"interval","interval_seconds":10}}`
	task := decodeSchedule(t, scheduleHTTP(t, s, "POST", "/schedules", "alice", body, 201))
	path := "/schedules/" + task.ScheduleID
	for _, query := range []string{"after=-1", "after=no", "limit=0", "limit=1001"} {
		scheduleHTTP(t, s, "GET", path+"/executions?"+query, "alice", "", 422)
	}
	scheduleHTTP(t, s, "PATCH", path, "alice", `{}`, 405)
	scheduleHTTP(t, s, "GET", path+"/unknown", "alice", "", 404)
	h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{}, deploymentError("access_denied")
	}
	scheduleHTTP(t, s, "POST", "/schedules", "alice", body, 403)
	scheduleHTTP(t, s, "GET", path, "alice", "", 200)
	task = decodeSchedule(t, scheduleHTTP(t, s, "POST", path+"/pause", "alice", `{"revision":0}`, 200))
	scheduleHTTP(t, s, "POST", path+"/resume", "alice", fmt.Sprintf(`{"revision":%d}`, task.Revision), 403)
	scheduleHTTP(t, s, "DELETE", path+fmt.Sprintf("?revision=%d", task.Revision), "alice", "", 204)
}

func TestScheduleHTTPWorkerDispatchesAndCompletes(t *testing.T) {
	s, h := scheduleServer(t, true)
	// A short future timestamp exercises the real worker clock and shutdown.
	body := fmt.Sprintf(`{"name":"Once","pack_id":"records","instruction":"Say done","schedule":{"kind":"once","at":%.6f}}`, unixNow()+0.1)
	task := decodeSchedule(t, scheduleHTTP(t, s, "POST", "/schedules", "alice", body, 201))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, err := h.GetSchedule(t.Context(), task.ScheduleID, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if current.LastExecution != nil && current.LastExecution.Status == "completed" {
			scheduleHTTP(t, s, "GET", "/readyz", "", "", 200)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not trigger and complete scheduled task")
}

func TestScheduleFailureDoesNotBlockOtherSchedulesOrRuns(t *testing.T) {
	now := 1000.
	h := scheduleHost(t, &now)
	bad := createSchedule(t, h, ScheduleSpec{Kind: "interval", IntervalSeconds: 60})
	request := scheduleRequest(ScheduleSpec{Kind: "interval", IntervalSeconds: 61})
	request.PackID = "healthy"
	good, err := h.CreateSchedule(t.Context(), "alice", request)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := h.Create(t.Context(), "alice", "healthy", "Say done", "")
	if err != nil {
		t.Fatal(err)
	}
	now = 1061.
	h.PolicyResolver = func(_ context.Context, _, pack string) (ExecutionPolicy, error) {
		if pack == "records" {
			return ExecutionPolicy{}, hostError("authorization_unavailable")
		}
		return ExecutionPolicy{AllowModelData: true}, nil
	}
	s, err := NewHTTPServer(h, &Deployment{}, true, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(s.Close)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, err := h.GetSchedule(t.Context(), good.ScheduleID, "alice")
		if err != nil {
			t.Fatal(err)
		}
		run, err := h.Get(t.Context(), ordinary.RunID, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if current.LastExecution != nil && current.LastExecution.Status == "completed" && run.Status == "completed" {
			failed, callErr := h.GetSchedule(t.Context(), bad.ScheduleID, "alice")
			if callErr != nil {
				t.Error(callErr)
			}
			if failed.LastExecution != nil || failed.Revision != 0 || *failed.NextRunAt != 1060 {
				t.Fatalf("failure consumed occurrence: %+v", failed)
			}
			// Readiness is updated after this cycle has driven both runs.
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				s.mu.RLock()
				failed := s.workerFailed
				s.mu.RUnlock()
				if failed {
					scheduleHTTP(t, s, "GET", "/readyz", "", "", 503)
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("worker failure was not reflected in readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("one failing schedule blocked healthy work")
}
