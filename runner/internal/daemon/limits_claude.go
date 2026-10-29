package daemon

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Claude's reactive limit signal. When the account's limit refuses a call,
// Claude Code writes an assistant line flagged as an API error (code
// "rate_limit") whose text says when the limit lifts, in one of the forms the
// CLI has used:
//
//	Claude AI usage limit reached|1792170000          (unix seconds)
//	5-hour limit reached ∙ resets 3pm
//	You've hit your limit · resets 3pm (America/Los_Angeles)
//	Weekly limit reached ∙ resets Oct 20, 3pm
//
// claudeRateLimitReset reads that time; the tracker then shows Claude
// "capped" until it.

// claudeCapHorizon bounds a believable reset: the longest window is a week.
const claudeCapHorizon = 8 * 24 * time.Hour

var (
	claudeResetUnixRE = regexp.MustCompile(`\|(\d{9,11})\b`)
	claudeResetTimeRE = regexp.MustCompile(`(?i)\bresets\s+(?:at\s+)?` +
		`(?:(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2}),?\s+(?:at\s+)?)?` +
		`(?:(\d{4}),?\s+(?:at\s+)?)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?(?:\s*\(([^)]+)\))?`)
	claudeMonths = map[string]time.Month{
		"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
		"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
		"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
	}
)

// claudeRateLimitReset is when the limit named in text lifts, if text names
// a time within a week or just passed (a re-refused continuation). Minute-only
// times round forward to the end of the printed minute. A bare clock is its
// next occurrence unless it just passed; a time zone in parentheses is honored,
// else now's zone (the daemon's local time, as the CLI's is).
func claudeRateLimitReset(text string, now time.Time) (time.Time, bool) {
	believable := func(t time.Time) (time.Time, bool) {
		if t.Before(now.Add(-10*time.Minute)) || t.Sub(now) > claudeCapHorizon {
			return time.Time{}, false
		}
		return t, true
	}
	if m := claudeResetUnixRE.FindStringSubmatch(text); m != nil {
		secs, err := strconv.ParseInt(m[1], 10, 64)
		if err == nil {
			return believable(time.Unix(secs, 0))
		}
	}
	m := claudeResetTimeRE.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	monthName, dayText, yearText, hourText, minText, ampm, zone := strings.ToLower(m[1]), m[2], m[3], m[4], m[5], strings.ToLower(m[6]), m[7]
	hour, _ := strconv.Atoi(hourText)
	minute := 0
	if minText != "" {
		minute, _ = strconv.Atoi(minText)
	}
	switch {
	case ampm != "":
		if hour < 1 || hour > 12 {
			return time.Time{}, false
		}
		hour %= 12
		if ampm == "pm" {
			hour += 12
		}
	case minText == "" || hour > 23: // "resets 3" says nothing
		return time.Time{}, false
	}
	if minute > 59 {
		return time.Time{}, false
	}
	loc := now.Location()
	if zone != "" {
		if l, err := time.LoadLocation(strings.TrimSpace(zone)); err == nil {
			loc = l
		}
	}
	local := now.In(loc)
	if monthName != "" {
		day, _ := strconv.Atoi(dayText)
		if day < 1 || day > 31 {
			return time.Time{}, false
		}
		years := []int{local.Year() - 1, local.Year(), local.Year() + 1}
		if yearText != "" {
			year, _ := strconv.Atoi(yearText)
			years = []int{year}
		}
		// A yearless date can belong to last year just after midnight on
		// January 1, as well as next year near the end of December.
		for _, year := range years {
			t := limitResetWallTime(year, claudeMonths[monthName], day, hour, minute, loc)
			if t.Month() != claudeMonths[monthName] || t.Day() != day {
				continue
			}
			if reset, ok := believable(t.Add(time.Minute)); ok {
				return reset, true
			}
		}
		return time.Time{}, false
	}
	// Just after midnight a bare clock may name yesterday's minute. Keep
	// that recent instant instead of moving a re-refusal almost a day ahead.
	yesterday := local.AddDate(0, 0, -1)
	previous := limitResetWallTime(yesterday.Year(), yesterday.Month(), yesterday.Day(), hour, minute, loc).Add(time.Minute)
	if !previous.After(now) && !previous.Before(now.Add(-10*time.Minute)) {
		return believable(previous)
	}
	t := limitResetWallTime(local.Year(), local.Month(), local.Day(), hour, minute, loc)
	if t.Before(now.Add(-10 * time.Minute)) {
		next := local.AddDate(0, 0, 1)
		t = limitResetWallTime(next.Year(), next.Month(), next.Day(), hour, minute, loc)
	}
	return believable(t.Add(time.Minute))
}

// During a backward clock change the CLI's printed local time names two
// instants. Choose the later one so a missing UTC offset cannot cause an early
// continuation. ZoneBounds also handles zones with a non-hour clock change.
func limitResetWallTime(year int, month time.Month, day, hour, minute int, loc *time.Location) time.Time {
	t := time.Date(year, month, day, hour, minute, 0, 0, loc)
	_, end := t.ZoneBounds()
	if end.IsZero() {
		return t
	}
	_, before := t.Zone()
	_, after := end.Zone()
	if before <= after {
		return t
	}
	later := t.Add(time.Duration(before-after) * time.Second)
	if later.Year() == year && later.Month() == month && later.Day() == day && later.Hour() == hour && later.Minute() == minute {
		return later
	}
	return t
}
