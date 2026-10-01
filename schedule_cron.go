package agenstra

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Keep IANA zones available in the standalone server/container.
)

// cronRule implements numeric, five-field cron: minute hour day month weekday.
// Day-of-month and weekday use the conventional OR rule when both are restricted.
type cronRule struct {
	fields             [5]uint64
	dayAny, weekdayAny bool
	location           *time.Location
}

func parseCron(expression, timezone string) (*cronRule, error) {
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron requires five fields")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil || timezone == "Local" || timezone == "" {
		return nil, fmt.Errorf("cron requires an IANA timezone")
	}
	rule := &cronRule{location: loc}
	minima := [5]int{0, 0, 1, 1, 0}
	maxima := [5]int{59, 23, 31, 12, 7}
	for i, part := range parts {
		mask, any, err := cronField(part, minima[i], maxima[i])
		if err != nil {
			return nil, err
		}
		if i == 4 && mask&(1<<7) != 0 {
			mask |= 1
			mask &^= 1 << 7
		}
		rule.fields[i] = mask
		if i == 2 {
			rule.dayAny = any
		}
		if i == 4 {
			rule.weekdayAny = any
		}
	}
	// Reject impossible calendars (e.g. February 31 with an unrestricted
	// weekday) before searching minute by minute for a future occurrence.
	possible := false
	for year := 2000; year < 2028 && !possible; year++ {
		for month := 1; month <= 12 && !possible; month++ {
			if !rule.has(3, month) {
				continue
			}
			for day := 1; day <= time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day(); day++ {
				if rule.matchesDay(time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)) {
					possible = true
					break
				}
			}
		}
	}
	if !possible {
		return nil, fmt.Errorf("cron has no possible date")
	}
	return rule, nil
}

func cronNumber(raw string) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty cron number")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid cron number")
		}
	}
	return strconv.Atoi(raw)
}

func cronField(raw string, minimum, maximum int) (uint64, bool, error) {
	var mask uint64
	any := false
	for _, item := range strings.Split(raw, ",") {
		step := 1
		base, stride, stepped := strings.Cut(item, "/")
		if stepped {
			var err error
			step, err = cronNumber(stride)
			if err != nil || step < 1 || step > maximum-minimum+1 {
				return 0, false, fmt.Errorf("invalid cron step")
			}
		}
		lo, hi := minimum, maximum
		var err error
		if base == "*" {
			any = true
		} else if start, end, ranged := strings.Cut(base, "-"); ranged {
			lo, err = cronNumber(start)
			if err == nil {
				hi, err = cronNumber(end)
			}
		} else {
			lo, err = cronNumber(base)
			if !stepped {
				hi = lo
			}
		}
		if err != nil || lo < minimum || hi > maximum || hi < lo {
			return 0, false, fmt.Errorf("invalid cron range")
		}
		for value := lo; value <= hi; value += step {
			mask |= uint64(1) << value
		}
	}
	return mask, any, nil
}

func (r *cronRule) has(field, value int) bool { return r.fields[field]&(uint64(1)<<value) != 0 }
func (r *cronRule) matchesDay(t time.Time) bool {
	day, weekday := r.has(2, t.Day()), r.has(4, int(t.Weekday()))
	if r.dayAny || r.weekdayAny {
		return day && weekday
	}
	return day || weekday
}

func (r *cronRule) next(after time.Time) (time.Time, error) {
	// Advance in absolute time so both occurrences of a repeated local minute
	// are eligible at a DST fallback; nonexistent local minutes are skipped.
	end := after.AddDate(8, 0, 0)
	for t := after.Truncate(time.Minute).Add(time.Minute); t.Before(end); t = t.Add(time.Minute) {
		local := t.In(r.location)
		if r.has(3, int(local.Month())) && r.matchesDay(local) && r.has(1, local.Hour()) && r.has(0, local.Minute()) {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cron has no occurrence in the next eight years")
}
