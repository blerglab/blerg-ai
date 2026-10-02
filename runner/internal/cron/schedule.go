// Package cron is the runner's cron engine: schedule parsing and slot
// iteration (this file) and the scheduler that fires due crons (scheduler.go).
//
// Expression parsing is delegated to github.com/robfig/cron/v3 (a bit-mask
// parser that accepts the usual field syntax). Its own Next() is NOT used: it
// skips a wall-clock slot that does not exist on a spring-forward day and fires
// a slot twice on a fall-back day, and the spec (7.2) needs exactly once in
// both. The iterator below reads the parsed bit masks and resolves wall-clock
// times to instants itself.
package cron

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	robcron "github.com/robfig/cron/v3"
)

// MinInterval is the shortest allowed gap between two occurrences of a
// schedule, checked over a full year.
const MinInterval = 15 * time.Minute

// intervalCheckSpan is how far ahead Validate looks for a too-close pair.
const intervalCheckSpan = 366 * 24 * time.Hour

// maxSearchDays bounds Next: a leap-day schedule can be eight years away
// (2096 to 2104), anything with no occurrence in nine years has none.
const maxSearchDays = 366 * 9

// starBit is robfig's marker for a field written as "*" or "?".
const starBit = 1 << 63

var fiveFieldParser = robcron.NewParser(
	robcron.Minute | robcron.Hour | robcron.Dom | robcron.Month | robcron.Dow)

var zoneNameRe = regexp.MustCompile(`^[A-Za-z0-9_+\-/]+$`)

// Schedule is a validated five-field cron expression in one IANA zone.
type Schedule struct {
	expr string
	tz   string
	spec *robcron.SpecSchedule
	loc  *time.Location
}

// ParseSchedule accepts only a plain five-field expression (minute hour
// day-of-month month day-of-week) and an IANA zone name. `@every`, other `@`
// descriptors, `CRON_TZ=`/`TZ=` prefixes and six-field forms are rejected.
// Whether the schedule ever fires, and how often, is Validate's business.
func ParseSchedule(expr, tz string) (*Schedule, error) {
	for _, r := range expr {
		if r < 0x20 || r > 0x7e {
			return nil, errors.New("schedule must be a plain five-field cron expression")
		}
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, errors.New("schedule must have exactly five fields: minute hour day-of-month month day-of-week")
	}
	if strings.HasPrefix(fields[0], "@") || strings.Contains(expr, "TZ=") {
		return nil, errors.New("schedule descriptors and TZ= prefixes are not supported; use five fields and the timezone setting")
	}
	loc, err := loadZone(tz)
	if err != nil {
		return nil, err
	}
	norm := strings.Join(fields, " ")
	parsed, err := fiveFieldParser.Parse(norm)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule: %w", err)
	}
	spec, ok := parsed.(*robcron.SpecSchedule)
	if !ok {
		return nil, errors.New("schedule must be a plain five-field cron expression")
	}
	return &Schedule{expr: norm, tz: tz, spec: spec, loc: loc}, nil
}

func loadZone(tz string) (*time.Location, error) {
	if tz == "" || tz == "Local" || !zoneNameRe.MatchString(tz) {
		return nil, errors.New("timezone must be an IANA zone name such as Europe/London or UTC")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", tz)
	}
	return loc, nil
}

// Expr is the normalised expression; TZ the zone name.
func (s *Schedule) Expr() string { return s.expr }

// TZ is the IANA zone name the schedule runs in.
func (s *Schedule) TZ() string { return s.tz }

// Validate rejects a schedule with no next occurrence after from (30 February)
// and one whose occurrences, over the following year, are ever closer than
// MinInterval (so `*/5 * 1 * *` cannot pass on the strength of its sparse days).
func (s *Schedule) Validate(from time.Time) error {
	prev, ok := s.Next(from)
	if !ok {
		return errors.New("schedule never runs (no matching date exists)")
	}
	end := from.Add(intervalCheckSpan)
	for {
		next, ok := s.Next(prev)
		if !ok || next.After(end) {
			return nil
		}
		if next.Sub(prev) < MinInterval {
			return fmt.Errorf("schedule runs more often than every 15 minutes (%s and %s)",
				prev.In(s.loc).Format("Mon 2006-01-02 15:04"), next.In(s.loc).Format("15:04"))
		}
		prev = next
	}
}

// Next is the first slot strictly after the given instant. A wall-clock slot
// inside a spring-forward gap becomes the first valid instant after it (the
// moment the clocks jump); a slot inside a fall-back overlap is the first of
// its two occurrences. Slots that collapse onto one instant count once. The
// bool is false when no slot exists within nine years.
func (s *Schedule) Next(after time.Time) (time.Time, bool) {
	local := after.In(s.loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxSearchDays; i++ {
		if s.dayMatches(day) {
			startHour := 0
			if i == 0 {
				// Offsets never move by more than a couple of hours, so
				// earlier wall hours cannot map past `after`.
				startHour = max(0, local.Hour()-3)
			}
			for h := startHour; h < 24; h++ {
				if s.spec.Hour&(1<<uint(h)) == 0 {
					continue
				}
				for m := 0; m < 60; m++ {
					if s.spec.Minute&(1<<uint(m)) == 0 {
						continue
					}
					inst := wallToInstant(s.loc, day.Year(), day.Month(), day.Day(), h, m)
					if inst.After(after) {
						return inst, true
					}
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}, false
}

// dayMatches applies the standard cron day rule: when both day fields are
// restricted a day matches if either does, otherwise both must.
func (s *Schedule) dayMatches(day time.Time) bool {
	if s.spec.Month&(1<<uint(day.Month())) == 0 {
		return false
	}
	domMatch := s.spec.Dom&(1<<uint(day.Day())) != 0
	dowMatch := s.spec.Dow&(1<<uint(day.Weekday())) != 0
	if s.spec.Dom&starBit != 0 || s.spec.Dow&starBit != 0 {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}

// wallToInstant turns a wall-clock time in loc into an instant. An ambiguous
// wall time (fall back) takes its first occurrence; a nonexistent one (spring
// forward) takes the moment the clocks jump.
func wallToInstant(loc *time.Location, y int, mo time.Month, d, h, mi int) time.Time {
	naive := time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	offBefore := offsetAt(loc, naive.Add(-24*time.Hour))
	offAfter := offsetAt(loc, naive.Add(24*time.Hour))
	var best time.Time
	found := false
	for _, off := range [2]int{offBefore, offAfter} {
		inst := naive.Add(-time.Duration(off) * time.Second)
		w := inst.In(loc)
		if w.Year() != y || w.Month() != mo || w.Day() != d || w.Hour() != h || w.Minute() != mi {
			continue
		}
		if !found || inst.Before(best) {
			best, found = inst, true
		}
	}
	if found {
		return best
	}
	// In a gap: find the instant the offset changes, between the two
	// candidates (which straddle it).
	lo := naive.Add(-time.Duration(max(offBefore, offAfter)) * time.Second)
	hi := naive.Add(-time.Duration(min(offBefore, offAfter)) * time.Second)
	base := offsetAt(loc, lo)
	loU, hiU := lo.Unix(), hi.Unix()
	for loU < hiU {
		mid := loU + (hiU-loU)/2
		if offsetAt(loc, time.Unix(mid, 0)) == base {
			loU = mid + 1
		} else {
			hiU = mid
		}
	}
	return time.Unix(loU, 0).UTC()
}

func offsetAt(loc *time.Location, t time.Time) int {
	_, off := t.In(loc).Zone()
	return off
}
