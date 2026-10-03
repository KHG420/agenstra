package agenstra

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
)

// ScheduleSpec defines either an absolute Unix timestamp, an interval in
// seconds, or a numeric five-field cron expression in an explicit IANA zone.
type ScheduleSpec struct {
	Kind            string   `json:"kind"`
	At              *float64 `json:"at,omitempty"`
	IntervalSeconds int      `json:"interval_seconds,omitempty"`
	Cron            string   `json:"cron,omitempty"`
	Timezone        string   `json:"timezone,omitempty"`
}

// ScheduleRequest is the complete editable definition of a scheduled task.
// Updates replace this definition and require the current revision.
type ScheduleRequest struct {
	Sources     []RunSource  `json:"sources,omitempty"`
	Name        string       `json:"name"`
	PackID      string       `json:"pack_id"`
	Instruction string       `json:"instruction"`
	Schedule    ScheduleSpec `json:"schedule"`
}

// ScheduledTask retains an owner's schedule, revision and next dispatch time.
type ScheduledTask struct {
	ScheduleID string `json:"schedule_id"`
	OwnerID    string `json:"owner_id"`
	ScheduleRequest
	Status        string             `json:"status"` // active, paused, completed
	Revision      int                `json:"revision"`
	NextRunAt     *float64           `json:"next_run_at"`
	CreatedAt     float64            `json:"created_at"`
	UpdatedAt     float64            `json:"updated_at"`
	LastExecution *ScheduleExecution `json:"last_execution,omitempty"`
}

// ScheduleExecution records one dispatch attempt. For attempts with a run,
// Status and ErrorCode reflect its current durable state. Fetch that run through
// AgentHost.Get (or /runs/{id}) to read results under the current authorization.
type ScheduleExecution struct {
	Sequence    int64   `json:"sequence"`
	ScheduleID  string  `json:"schedule_id"`
	ScheduledAt float64 `json:"scheduled_at"`
	TriggeredAt float64 `json:"triggered_at"`
	RunID       string  `json:"run_id,omitempty"`
	Status      string  `json:"status"`
	ErrorCode   string  `json:"error_code,omitempty"`
}

func scheduleTime(timestamp float64) time.Time {
	seconds := math.Floor(timestamp)
	return time.Unix(int64(seconds), int64((timestamp-seconds)*1e9)).UTC()
}
func scheduleTimestamp(t time.Time) float64 { return float64(t.Unix()) + float64(t.Nanosecond())/1e9 }

func (r ScheduleRequest) normalized(now float64) (ScheduleRequest, float64, error) {
	if strings.TrimSpace(r.Name) == "" || len([]rune(r.Name)) > 200 || strings.TrimSpace(r.PackID) == "" || len(r.PackID) > 128 || strings.TrimSpace(r.Instruction) == "" || len([]rune(r.Instruction)) > 30000 {
		return r, 0, hostError("schedule_invalid")
	}
	sources, err := normalizedSources(r.PackID, r.Sources)
	if err != nil {
		return r, 0, err
	}
	r.Sources = sources
	spec := &r.Schedule
	switch spec.Kind {
	case "once":
		if spec.At == nil || math.IsNaN(*spec.At) || math.IsInf(*spec.At, 0) || *spec.At <= now || *spec.At > 253402300799 || spec.IntervalSeconds != 0 || spec.Cron != "" || spec.Timezone != "" {
			return r, 0, hostError("schedule_invalid")
		}
		at := *spec.At
		spec.At = &at
		return r, at, nil
	case "interval":
		if spec.IntervalSeconds < 1 || spec.IntervalSeconds > 31536000 || spec.At != nil || spec.Cron != "" || spec.Timezone != "" {
			return r, 0, hostError("schedule_invalid")
		}
		return r, now + float64(spec.IntervalSeconds), nil
	case "cron":
		if spec.At != nil || spec.IntervalSeconds != 0 || len(spec.Cron) > 256 || len(spec.Timezone) > 100 {
			return r, 0, hostError("schedule_invalid")
		}
		spec.Cron = strings.Join(strings.Fields(spec.Cron), " ")
		if spec.Timezone == "" {
			spec.Timezone = "UTC"
		}
		rule, err := parseCron(spec.Cron, spec.Timezone)
		if err != nil {
			return r, 0, hostError("schedule_invalid")
		}
		next, err := rule.next(scheduleTime(now))
		if err != nil {
			return r, 0, hostError("schedule_invalid")
		}
		return r, scheduleTimestamp(next), nil
	default:
		return r, 0, hostError("schedule_invalid")
	}
}

// nextSchedule coalesces missed recurring occurrences into this one dispatch,
// keeping intervals anchored to the original cadence rather than worker delay.
func nextSchedule(spec ScheduleSpec, due, now float64) (*float64, error) {
	var next float64
	switch spec.Kind {
	case "once":
		return nil, nil
	case "interval":
		if spec.IntervalSeconds < 1 {
			return nil, hostError("schedule_invalid")
		}
		interval := float64(spec.IntervalSeconds)
		next = due + (math.Floor((now-due)/interval)+1)*interval
	case "cron":
		rule, err := parseCron(spec.Cron, spec.Timezone)
		if err != nil {
			return nil, hostError("schedule_invalid")
		}
		t, err := rule.next(scheduleTime(now))
		if err != nil {
			return nil, hostError("schedule_invalid")
		}
		next = scheduleTimestamp(t)
	default:
		return nil, hostError("schedule_invalid")
	}
	return &next, nil
}

// CreateSchedule authorizes and saves a normalized schedule with its request identity.
func (h *AgentHost) CreateSchedule(ctx context.Context, owner string, request ScheduleRequest) (ScheduledTask, error) {
	now := h.now()
	request, next, err := request.normalized(now)
	if err != nil {
		return ScheduledTask{}, err
	}
	if _, err = h.bindSources(ctx, owner, request.PackID, request.Sources); err != nil {
		return ScheduledTask{}, err
	}
	task := ScheduledTask{ScheduleID: NewID(), OwnerID: owner, ScheduleRequest: request, Status: "active", NextRunAt: &next, CreatedAt: now, UpdatedAt: now}
	return task, h.Store.insertSchedule(task)
}

// GetSchedule remains available to the owner after a pack grant is revoked,
// so the owner can inspect, pause or remove the affected schedule.
func (h *AgentHost) GetSchedule(ctx context.Context, id, owner string) (ScheduledTask, error) {
	if err := ctx.Err(); err != nil {
		return ScheduledTask{}, err
	}
	return h.Store.getSchedule(id, owner)
}

// ListSchedules returns a bounded list of the owner's schedules.
func (h *AgentHost) ListSchedules(ctx context.Context, owner string, limit int) ([]ScheduledTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, hostError("invalid_limit")
	}
	return h.Store.listSchedules(owner, limit)
}

// UpdateSchedule authorizes an edit against the current schedule revision.
func (h *AgentHost) UpdateSchedule(ctx context.Context, id, owner string, revision int, request ScheduleRequest) (ScheduledTask, error) {
	task, err := h.GetSchedule(ctx, id, owner)
	if err != nil {
		return task, err
	}
	request, next, err := request.normalized(h.now())
	if err != nil {
		return task, err
	}
	if _, err = h.bindSources(ctx, owner, request.PackID, request.Sources); err != nil {
		return task, err
	}
	task.ScheduleRequest, task.NextRunAt = request, &next
	if task.Status != "paused" {
		task.Status = "active"
	}
	return h.Store.replaceSchedule(task, revision, h.now())
}

// PauseSchedule stops future dispatches under an expected revision.
func (h *AgentHost) PauseSchedule(ctx context.Context, id, owner string, revision int) (ScheduledTask, error) {
	task, err := h.GetSchedule(ctx, id, owner)
	if err != nil {
		return task, err
	}
	if task.Status == "completed" {
		return task, hostError("schedule_completed")
	}
	task.Status = "paused"
	return h.Store.replaceSchedule(task, revision, h.now())
}

// ResumeSchedule recomputes the next dispatch after checking current access and revision.
func (h *AgentHost) ResumeSchedule(ctx context.Context, id, owner string, revision int) (ScheduledTask, error) {
	task, err := h.GetSchedule(ctx, id, owner)
	if err != nil {
		return task, err
	}
	if task.Status == "completed" {
		return task, hostError("schedule_completed")
	}
	if _, err = h.bindSources(ctx, owner, task.PackID, task.Sources); err != nil {
		return task, err
	}
	if task.Status == "paused" && task.Schedule.Kind != "once" {
		task.NextRunAt, err = nextSchedule(task.Schedule, *task.NextRunAt, h.now())
		if err != nil {
			return task, err
		}
	}
	task.Status = "active"
	return h.Store.replaceSchedule(task, revision, h.now())
}

// DeleteSchedule removes the schedule under its expected revision; existing runs retain their evidence.
func (h *AgentHost) DeleteSchedule(ctx context.Context, id, owner string, revision int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.Store.deleteSchedule(id, owner, revision)
}

// ListScheduleExecutions returns an owner-scoped page of saved dispatches after a cursor.
func (h *AgentHost) ListScheduleExecutions(ctx context.Context, id, owner string, after int64, limit int) ([]ScheduleExecution, error) {
	if _, err := h.GetSchedule(ctx, id, owner); err != nil {
		return nil, err
	}
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, hostError("invalid_page")
	}
	return h.Store.scheduleExecutions(id, owner, after, limit, false)
}

// DispatchDueSchedules creates durable queued runs without driving the model.
// Embedded hosts call this before WakeDue; the HTTP server worker does both.
// Creation, history and advancement commit atomically. A competing tick or an
// edit/pause/delete during authorization cannot dispatch the stale definition.
func (h *AgentHost) DispatchDueSchedules(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, hostError("invalid_limit")
	}
	tasks, err := h.Store.dueSchedules(h.now(), limit)
	if err != nil {
		return 0, err
	}
	count := 0
	var firstErr error
	for _, task := range tasks {
		if err = ctx.Err(); err != nil {
			return count, err
		}
		runID := NewID()
		state, prepareErr := h.prepareRun(ctx, task.OwnerID, task.PackID, task.Instruction, runID)
		if prepareErr == nil && len(task.Sources) > 0 {
			bindings, err := h.bindSources(ctx, task.OwnerID, task.PackID, task.Sources)
			prepareErr = err
			state["project_sources"] = bindings
		}
		if prepareErr == nil {
			// Dispatch, pause and resume also advance Revision. Count a definition's
			// instruction once, rather than treating each tick as fresh user evidence.
			setMemoryInput(state, "schedule:"+task.ScheduleID+":"+webHash(task.PackID+"\x00"+task.Instruction), task.Instruction)
		}
		if err = ctx.Err(); err != nil {
			return count, err
		}
		// Permission failures pause the definition. Infrastructure failures leave
		// it due for retry and surface through worker readiness.
		if prepareErr != nil {
			switch ErrorCode(prepareErr) {
			case "access_denied", "forbidden", "identity_unverified", "model_data_not_authorized", "capability_not_granted", "source_scope_invalid":
			default:
				if firstErr == nil {
					firstErr = prepareErr
				}
				continue
			}
		}
		now := h.now()
		next, err := nextSchedule(task.Schedule, *task.NextRunAt, now)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		err = h.Store.dispatchSchedule(task, runID, state, prepareErr, next, now)
		if errors.Is(err, ErrStoreConflict) || ErrorCode(err) == "not_found" {
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		count++
	}
	return count, firstErr
}
