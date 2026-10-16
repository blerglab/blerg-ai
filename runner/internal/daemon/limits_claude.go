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
		`(\d{1,2})(?::(\d{2}))?\s*(am|pm)?(?:\s*\(([^)]+)\))?`)
	claudeMonths = map[string]time.Month{
		"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
		"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
		"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
	}
)

// claudeRateLimitReset is when the limit named in text lifts, if text names
// a time after now and within a week or so. A clock time without a date is
// its next occurrence; a time zone in parentheses is honoured when known,
// else now's zone (the daemon's local time, as the CLI's is).
func claudeRateLimitReset(text string, now time.Time) (time.Time, bool) {
	believable := func(t time.Time) (time.Time, bool) {
		if !t.After(now) || t.Sub(now) > claudeCapHorizon {
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
	monthName, dayText, hourText, minText, ampm, zone := strings.ToLower(m[1]), m[2], m[3], m[4], strings.ToLower(m[5]), m[6]
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
		t := time.Date(local.Year(), claudeMonths[monthName], day, hour, minute, 0, 0, loc)
		if !t.After(now) {
			t = t.AddDate(1, 0, 0)
		}
		return believable(t)
	}
	t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return believable(t)
}
