package cron

import (
	"testing"
	"time"
)

func TestCronTimezone(t *testing.T) {
	for _, tc := range []struct {
		expr, zone, at string
		want           bool
	}{
		{"*/30 * * * *", "Asia/Shanghai", "2026-10-29T01:30:00Z", true},
		{"0 9 * * *", "Asia/Shanghai", "2026-10-29T01:00:00Z", true},
		{"0 9 * * *", "UTC", "2026-10-29T01:00:00Z", false},
		{"0 9 * * 1-5", "Asia/Shanghai", "2026-10-31T01:00:00Z", false},
		{"0 9 1 * 4", "UTC", "2026-10-29T09:00:00Z", true},
		{"0 9 * * 7", "UTC", "2026-11-01T09:00:00Z", true},
		{"* * */9223372036854775807 * *", "UTC", "2026-11-01T09:00:00Z", true},
		{"30 1 * * *", "America/New_York", "2026-11-01T05:30:00Z", true},
		{"30 1 * * *", "America/New_York", "2026-11-01T06:30:00Z", true},
	} {
		s, err := Parse(tc.expr, tc.zone)
		if err != nil {
			t.Fatal(err)
		}
		at, _ := time.Parse(time.RFC3339, tc.at)
		if s.Matches(at) != tc.want {
			t.Fatalf("%+v", tc)
		}
	}
}
func TestInvalidCron(t *testing.T) {
	for _, expr := range []string{"* * * *", "* * * * * *", "60 * * * *", "*/0 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "1-0 * * * *", "a * * * *", "* * * * 1/2/3"} {
		if _, err := Parse(expr, "UTC"); err == nil {
			t.Fatalf("accepted %s", expr)
		}
	}
	for _, zone := range []string{"Mars/Local", "Local", ""} {
		if _, err := Parse("* * * * *", zone); err == nil {
			t.Fatalf("accepted timezone %q", zone)
		}
	}
}
