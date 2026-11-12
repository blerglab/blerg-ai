// Package persontime describes the person's account clock, independently of server time.
package persontime

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Host daemons and slim containers use the same IANA rules.
)

type Account struct {
	Timezone   string  `json:"timezone"`
	QuietHours *Window `json:"quiet_hours,omitempty"`
}

func ValidZone(zone string) bool {
	if zone == "" || (zone != "UTC" && !strings.Contains(zone, "/")) || strings.TrimSpace(zone) != zone || len(zone) > 128 {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}

func (a Account) Line(now time.Time) string {
	if !ValidZone(a.Timezone) {
		return ""
	}
	loc, _ := time.LoadLocation(a.Timezone)
	line := fmt.Sprintf("Person's local time: %s (%s)", now.In(loc).Format("Mon 2006-01-02 15:04 MST"), a.Timezone)
	if a.InQuietHours(now) {
		line += "; quiet hours; person likely away"
	}
	return line
}

// Window is a daily wall-clock interval in the account's IANA zone.
type Window struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

func minute(value string) (int, bool) {
	if len(value) != 5 || value[2] != ':' {
		return 0, false
	}
	hour, e1 := strconv.Atoi(value[:2])
	minutes, e2 := strconv.Atoi(value[3:])
	if e1 != nil || e2 != nil || hour < 0 || hour > 23 || minutes < 0 || minutes > 59 || fmt.Sprintf("%02d:%02d", hour, minutes) != value {
		return 0, false
	}
	return hour*60 + minutes, true
}
func (a Account) Valid() bool {
	if !ValidZone(a.Timezone) {
		return false
	}
	if a.QuietHours == nil {
		return true
	}
	start, ok1 := minute(a.QuietHours.Start)
	end, ok2 := minute(a.QuietHours.End)
	return ok1 && ok2 && (!a.QuietHours.Enabled || start != end)
}

// InQuietHours uses local wall time: both copies of a fall-back hour match, and a
// skipped spring hour needs no special scheduling. The end minute is exclusive.
func (a Account) InQuietHours(now time.Time) bool {
	if !a.Valid() || a.QuietHours == nil || !a.QuietHours.Enabled {
		return false
	}
	loc, _ := time.LoadLocation(a.Timezone)
	local := now.In(loc)
	current := local.Hour()*60 + local.Minute()
	start, _ := minute(a.QuietHours.Start)
	end, _ := minute(a.QuietHours.End)
	if start < end {
		return current >= start && current < end
	}
	return current >= start || current < end
}

// QuietEnd returns the first instant outside this window. Walking local minutes
// handles missing/repeated DST endpoints without assuming a 24-hour day.
func (a Account) QuietEnd(now time.Time) time.Time {
	if !a.InQuietHours(now) {
		return now
	}
	loc, _ := time.LoadLocation(a.Timezone)
	start, _ := minute(a.QuietHours.Start)
	end, _ := minute(a.QuietHours.End)
	for candidate := now.Truncate(time.Minute).Add(time.Minute); candidate.Before(now.Add(27 * time.Hour)); candidate = candidate.Add(time.Minute) {
		local := candidate.In(loc)
		m := local.Hour()*60 + local.Minute()
		quiet := (start < end && m >= start && m < end) || (start >= end && (m >= start || m < end))
		if !quiet {
			return candidate
		}
	}
	return now.Add(27 * time.Hour)
}
