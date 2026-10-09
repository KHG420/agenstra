package host

import (
	"context"
	"errors"
	"math"

	"github.com/KHG420/agenstra/internal/base/cron"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

// nextSchedule coalesces missed recurring occurrences into this one dispatch,
// keeping intervals anchored to the original cadence rather than worker delay.
func nextSchedule(spec agentcontract.ScheduleSpec, due, now float64) (*float64, error) {
	var next float64
	switch spec.Kind {
	case "once":
		return nil, nil
	case "interval":
		if spec.IntervalSeconds < 1 {
			return nil, agentcontract.NewHostError("schedule_invalid")
		}
		interval := float64(spec.IntervalSeconds)
		next = due + (math.Floor((now-due)/interval)+1)*interval
	case "cron":
		rule, err := cron.Parse(spec.Cron, spec.Timezone)
		if err != nil {
			return nil, agentcontract.NewHostError("schedule_invalid")
		}
		t, err := rule.Next(agentcontract.ScheduleTime(now))
		if err != nil {
			return nil, agentcontract.NewHostError("schedule_invalid")
		}
		next = agentcontract.ScheduleTimestamp(t)
	default:
		return nil, agentcontract.NewHostError("schedule_invalid")
	}
	return &next, nil
}

// CreateSchedule authorizes and saves a normalized schedule with its request identity.
func (h *AgentHost) CreateSchedule(ctx context.Context, owner string, request agentcontract.ScheduleRequest) (agentcontract.ScheduledTask, error) {
	now := h.Now()
	request, next, err := request.Normalized(now)
	if err != nil {
		return agentcontract.ScheduledTask{}, err
	}
	if _, err = h.BindSources(ctx, owner, request.PackID, request.Sources); err != nil {
		return agentcontract.ScheduledTask{}, err
	}
	task := agentcontract.ScheduledTask{ScheduleID: agentcontract.NewID(), OwnerID: owner, ScheduleRequest: request, Status: "active", NextRunAt: &next, CreatedAt: now, UpdatedAt: now}
	return task, h.Store.InsertSchedule(task)
}

// GetSchedule remains available to the owner after a pack grant is revoked,
// so the owner can inspect, pause or remove the affected schedule.
func (h *AgentHost) GetSchedule(ctx context.Context, id, owner string) (agentcontract.ScheduledTask, error) {
	if err := ctx.Err(); err != nil {
		return agentcontract.ScheduledTask{}, err
	}
	return h.Store.GetSchedule(id, owner)
}

// ListSchedules returns a bounded list of the owner's schedules.
func (h *AgentHost) ListSchedules(ctx context.Context, owner string, limit int) ([]agentcontract.ScheduledTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, agentcontract.NewHostError("invalid_limit")
	}
	return h.Store.ListSchedules(owner, limit)
}

// UpdateSchedule authorizes an edit against the current schedule revision.
func (h *AgentHost) UpdateSchedule(ctx context.Context, id, owner string, revision int, request agentcontract.ScheduleRequest) (agentcontract.ScheduledTask, error) {
	task, err := h.GetSchedule(ctx, id, owner)
	if err != nil {
		return task, err
	}
	request, next, err := request.Normalized(h.Now())
	if err != nil {
		return task, err
	}
	if _, err = h.BindSources(ctx, owner, request.PackID, request.Sources); err != nil {
		return task, err
	}
	task.ScheduleRequest, task.NextRunAt = request, &next
	if task.Status != "paused" {
		task.Status = "active"
	}
	return h.Store.ReplaceSchedule(task, revision, h.Now())
}

// PauseSchedule stops future dispatches under an expected revision.
func (h *AgentHost) PauseSchedule(ctx context.Context, id, owner string, revision int) (agentcontract.ScheduledTask, error) {
	task, err := h.GetSchedule(ctx, id, owner)
	if err != nil {
		return task, err
	}
	if task.Status == "completed" {
		return task, agentcontract.NewHostError("schedule_completed")
	}
	task.Status = "paused"
	return h.Store.ReplaceSchedule(task, revision, h.Now())
}

// ResumeSchedule recomputes the next dispatch after checking current access and revision.
func (h *AgentHost) ResumeSchedule(ctx context.Context, id, owner string, revision int) (agentcontract.ScheduledTask, error) {
	task, err := h.GetSchedule(ctx, id, owner)
	if err != nil {
		return task, err
	}
	if task.Status == "completed" {
		return task, agentcontract.NewHostError("schedule_completed")
	}
	if _, err = h.BindSources(ctx, owner, task.PackID, task.Sources); err != nil {
		return task, err
	}
	if task.Status == "paused" && task.Schedule.Kind != "once" {
		task.NextRunAt, err = nextSchedule(task.Schedule, *task.NextRunAt, h.Now())
		if err != nil {
			return task, err
		}
	}
	task.Status = "active"
	return h.Store.ReplaceSchedule(task, revision, h.Now())
}

// DeleteSchedule removes the schedule under its expected revision; existing runs retain their evidence.
func (h *AgentHost) DeleteSchedule(ctx context.Context, id, owner string, revision int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.Store.DeleteSchedule(id, owner, revision)
}

// ListScheduleExecutions returns an owner-scoped page of saved dispatches after a cursor.
func (h *AgentHost) ListScheduleExecutions(ctx context.Context, id, owner string, after int64, limit int) ([]agentcontract.ScheduleExecution, error) {
	if _, err := h.GetSchedule(ctx, id, owner); err != nil {
		return nil, err
	}
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, agentcontract.NewHostError("invalid_page")
	}
	return h.Store.ScheduleExecutions(id, owner, after, limit, false)
}

// DispatchDueSchedules creates durable queued runs without driving the model.
// Embedded hosts call this before WakeDue; the HTTP server worker does both.
// Creation, history and advancement commit atomically. A competing tick or an
// edit/pause/delete during authorization cannot dispatch the stale definition.
func (h *AgentHost) DispatchDueSchedules(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, agentcontract.NewHostError("invalid_limit")
	}
	tasks, err := h.Store.DueSchedules(h.Now(), limit)
	if err != nil {
		return 0, err
	}
	count := 0
	var firstErr error
	for _, task := range tasks {
		if err = ctx.Err(); err != nil {
			return count, err
		}
		runID := agentcontract.NewID()
		state, prepareErr := h.prepareRun(ctx, task.OwnerID, task.PackID, task.Instruction, runID)
		if prepareErr == nil && len(task.Sources) > 0 {
			bindings, err := h.BindSources(ctx, task.OwnerID, task.PackID, task.Sources)
			prepareErr = err
			state["project_sources"] = bindings
		}
		if prepareErr == nil {

			// Dispatch, pause and resume also advance Revision. Count a definition's
			// instruction once, rather than treating each tick as fresh user evidence.
			setMemoryInput(state, "schedule:"+task.ScheduleID+":"+agentcontract.WebHash(task.PackID+"\x00"+task.Instruction), task.Instruction)
		}
		if err = ctx.Err(); err != nil {
			return count, err
		}

		// Permission failures pause the definition. Infrastructure failures leave
		// it due for retry and surface through worker readiness.
		if prepareErr != nil {
			switch agentcontract.ErrorCode(prepareErr) {
			case "access_denied", "forbidden", "identity_unverified", "model_data_not_authorized", "capability_not_granted", "source_scope_invalid":
			default:
				if firstErr == nil {
					firstErr = prepareErr
				}
				continue
			}
		}
		now := h.Now()
		next, err := nextSchedule(task.Schedule, *task.NextRunAt, now)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		err = h.Store.DispatchSchedule(task, runID, state, prepareErr, next, now)
		if errors.Is(err, runstore.ErrStoreConflict) || agentcontract.ErrorCode(err) == "not_found" {
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
