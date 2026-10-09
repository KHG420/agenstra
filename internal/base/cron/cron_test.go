package cron

import (
	"testing"
	"time"
)

func TestCronCalendarTimezoneAndDST(t *testing.T) {
	for _, tc := range []struct{ expression, zone, after, want string }{
		{"0 9 * * *", "Asia/Shanghai", "2026-10-01T00:59:59Z", "2026-10-01T01:00:00Z"},
		{"0 9 * * *", "Asia/Shanghai", "2026-10-01T01:00:00Z", "2026-10-02T01:00:00Z"},
		{"*/15 9-10 * * 1-5", "UTC", "2026-10-02T09:16:00Z", "2026-10-02T09:30:00Z"},
		{"0 0 1 * 1", "UTC", "2026-10-02T00:00:00Z", "2026-10-05T00:00:00Z"},
		{"0 0 * * 7", "UTC", "2026-10-02T00:00:00Z", "2026-10-04T00:00:00Z"},
		{"0 0 29 2 *", "UTC", "2026-10-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"0 0 29 2 *", "UTC", "2096-03-01T00:00:00Z", "2104-02-29T00:00:00Z"},
		{"30 2 * * *", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-09T06:30:00Z"},
		{"30 1 * * *", "America/New_York", "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z"},
		{"30 1 * * *", "America/New_York", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
	} {
		t.Run(tc.expression+tc.zone+tc.after, func(t *testing.T) {
			rule, err := Parse(tc.expression, tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			after, callErr := time.Parse(time.RFC3339, tc.after)
			if callErr != nil {
				t.Error(callErr)
			}
			got, err := rule.Next(after)
			if err != nil || got.UTC().Format(time.RFC3339) != tc.want {
				t.Fatalf("got %s %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestParseRejectsInvalidFieldsAndCalendars(t *testing.T) {
	for _, expression := range []string{"* * * * * *", "61 * * * *", "*/0 * * * *", "0 0 31 2 *", "0 0 * * MON"} {
		if _, err := Parse(expression, "UTC"); err == nil {
			t.Fatal("invalid cron accepted", expression)
		}
	}
	for _, zone := range []string{"", "Local", "Mars/City"} {
		if _, err := Parse("0 9 * * *", zone); err == nil {
			t.Fatal("invalid timezone accepted", zone)
		}
	}
}
