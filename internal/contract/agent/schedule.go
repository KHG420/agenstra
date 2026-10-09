package agent

import (
	"math"
	"strings"
	"time"

	"github.com/KHG420/agenstra/internal/base/cron"
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

// ScheduleTime converts a Unix-seconds timestamp, including its fractional part, to time.Time.
func ScheduleTime(timestamp float64) time.Time {
	seconds := math.Floor(timestamp)
	return time.Unix(int64(seconds), int64((timestamp-seconds)*1e9)).UTC()
}

// ScheduleTimestamp converts time.Time to a fractional Unix-seconds timestamp.
func ScheduleTimestamp(t time.Time) float64 { return float64(t.Unix()) + float64(t.Nanosecond())/1e9 }

// Normalized validates and normalizes the full schedule definition before storage.
func (r ScheduleRequest) Normalized(now float64) (ScheduleRequest, float64, error) {
	if strings.TrimSpace(r.Name) == "" || len([]rune(r.Name)) > 200 || strings.TrimSpace(r.PackID) == "" || len(r.PackID) > 128 || strings.TrimSpace(r.Instruction) == "" || len([]rune(r.Instruction)) > 30000 {
		return r, 0, NewHostError("schedule_invalid")
	}
	sources, err := NormalizedSources(r.PackID, r.Sources)
	if err != nil {
		return r, 0, err
	}
	r.Sources = sources
	spec := &r.Schedule
	switch spec.Kind {
	case "once":
		if spec.At == nil || math.IsNaN(*spec.At) || math.IsInf(*spec.At, 0) || *spec.At <= now || *spec.At > 253402300799 || spec.IntervalSeconds != 0 || spec.Cron != "" || spec.Timezone != "" {
			return r, 0, NewHostError("schedule_invalid")
		}
		at := *spec.At
		spec.At = &at
		return r, at, nil
	case "interval":
		if spec.IntervalSeconds < 1 || spec.IntervalSeconds > 31536000 || spec.At != nil || spec.Cron != "" || spec.Timezone != "" {
			return r, 0, NewHostError("schedule_invalid")
		}
		return r, now + float64(spec.IntervalSeconds), nil
	case "cron":
		if spec.At != nil || spec.IntervalSeconds != 0 || len(spec.Cron) > 256 || len(spec.Timezone) > 100 {
			return r, 0, NewHostError("schedule_invalid")
		}
		spec.Cron = strings.Join(strings.Fields(spec.Cron), " ")
		if spec.Timezone == "" {
			spec.Timezone = "UTC"
		}
		rule, err := cron.Parse(spec.Cron, spec.Timezone)
		if err != nil {
			return r, 0, NewHostError("schedule_invalid")
		}
		next, err := rule.Next(ScheduleTime(now))
		if err != nil {
			return r, 0, NewHostError("schedule_invalid")
		}
		return r, ScheduleTimestamp(next), nil
	default:
		return r, 0, NewHostError("schedule_invalid")
	}
}
