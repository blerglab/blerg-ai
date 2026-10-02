package cron

import (
	"strings"
	"testing"
	"time"
)

func mustSchedule(t *testing.T, expr, tz string) *Schedule {
	t.Helper()
	s, err := ParseSchedule(expr, tz)
	if err != nil {
		t.Fatalf("ParseSchedule(%q, %q): %v", expr, tz, err)
	}
	return s
}

func utc(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func TestParseScheduleAccepts(t *testing.T) {
	for _, tc := range []struct{ expr, tz string }{
		{"0 9 * * 1-5", "America/New_York"},
		{"*/15 * * * *", "UTC"},
		{"0 8,12,18 * * *", "Europe/London"},
		{"30 6 * * mon", "UTC"},
		{"0 0 1 jan *", "Asia/Kolkata"},
		{"0 0 29 2 *", "UTC"}, // leap day only: distant but real
		{"  0   9 * * *  ", "UTC"},
	} {
		s, err := ParseSchedule(tc.expr, tc.tz)
		if err != nil {
			t.Errorf("%q in %q rejected: %v", tc.expr, tc.tz, err)
			continue
		}
		if err := s.Validate(utc(2026, 1, 1, 0, 0)); err != nil {
			t.Errorf("%q in %q: Validate: %v", tc.expr, tc.tz, err)
		}
	}
}

func TestParseScheduleRejectsForms(t *testing.T) {
	for _, tc := range []struct{ name, expr, tz string }{
		{"empty", "", "UTC"},
		{"every", "@every 1h", "UTC"},
		{"every long", "@every 30m", "UTC"},
		{"daily", "@daily", "UTC"},
		{"hourly", "@hourly", "UTC"},
		{"midnight", "@midnight", "UTC"},
		{"cron tz prefix", "CRON_TZ=Asia/Tokyo 0 9 * * *", "UTC"},
		{"tz prefix", "TZ=UTC 0 9 * * *", "UTC"},
		{"six fields", "0 0 9 * * *", "UTC"},
		{"four fields", "0 9 * *", "UTC"},
		{"dow seven", "0 9 * * 7", "UTC"},
		{"garbage", "a b c d e", "UTC"},
		{"control char", "0 9 * * *\n", "UTC"},
		{"no tz", "0 9 * * *", ""},
		{"bad tz", "0 9 * * *", "Mars/Olympus"},
		{"local tz", "0 9 * * *", "Local"},
	} {
		if _, err := ParseSchedule(tc.expr, tc.tz); err == nil {
			t.Errorf("%s: %q in %q was accepted", tc.name, tc.expr, tc.tz)
		}
	}
}

func TestValidateRejectsNoOccurrence(t *testing.T) {
	for _, expr := range []string{"0 0 30 2 *", "0 0 31 4 *", "0 0 31 2 *"} {
		s := mustSchedule(t, expr, "UTC")
		if err := s.Validate(utc(2026, 1, 1, 0, 0)); err == nil {
			t.Errorf("%q has no occurrence but Validate accepted it", expr)
		}
		if _, ok := s.Next(utc(2026, 1, 1, 0, 0)); ok {
			t.Errorf("%q: Next reported an occurrence", expr)
		}
	}
}

func TestValidateMinimumInterval(t *testing.T) {
	from := utc(2026, 1, 1, 0, 0)
	for _, expr := range []string{
		"* * * * *",
		"*/5 * * * *",
		"*/14 * * * *",
		"0,10 * * * *",
		"*/5 * 1 * *", // only the first of the month, but still five minutes apart
		"0,5 3 * * *",
	} {
		s := mustSchedule(t, expr, "UTC")
		if err := s.Validate(from); err == nil {
			t.Errorf("%q is faster than 15 minutes but Validate accepted it", expr)
		} else if !strings.Contains(err.Error(), "15 minutes") {
			t.Errorf("%q: unhelpful error %q", expr, err)
		}
	}
	for _, expr := range []string{"*/15 * * * *", "0,15,30,45 * * * *", "0 * * * *", "0 9 * * *"} {
		if err := mustSchedule(t, expr, "UTC").Validate(from); err != nil {
			t.Errorf("%q should be accepted: %v", expr, err)
		}
	}
}

// The minimum applies to the whole year, so a constraint that only bites on a
// rare day cannot slip through a check of the next few occurrences.
func TestValidateMinimumIntervalLateInYear(t *testing.T) {
	s := mustSchedule(t, "*/5 * 31 12 *", "UTC")
	if err := s.Validate(utc(2026, 1, 1, 0, 0)); err == nil {
		t.Fatal("a schedule that is dense only on 31 December was accepted")
	}
}

func TestNextIsStrictlyAfter(t *testing.T) {
	s := mustSchedule(t, "0 9 * * *", "UTC")
	slot := utc(2026, 5, 1, 9, 0)
	next, ok := s.Next(slot)
	if !ok || !next.Equal(utc(2026, 5, 2, 9, 0)) {
		t.Fatalf("Next(slot) = %v %v, want the next day", next, ok)
	}
	next, ok = s.Next(slot.Add(-time.Nanosecond))
	if !ok || !next.Equal(slot) {
		t.Fatalf("Next(just before) = %v %v, want the slot itself", next, ok)
	}
}

func TestNextDayOfMonthAndWeekSemantics(t *testing.T) {
	// Both restricted: either matches (standard cron). 1 May 2026 is a Friday.
	s := mustSchedule(t, "0 0 1 * 1", "UTC")
	got := []time.Time{}
	cur := utc(2026, 4, 30, 0, 0)
	for i := 0; i < 3; i++ {
		n, _ := s.Next(cur)
		got = append(got, n)
		cur = n
	}
	want := []time.Time{utc(2026, 5, 1, 0, 0), utc(2026, 5, 4, 0, 0), utc(2026, 5, 11, 0, 0)}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("slot %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestNextLeapDay(t *testing.T) {
	s := mustSchedule(t, "0 0 29 2 *", "UTC")
	n, ok := s.Next(utc(2096, 3, 1, 0, 0))
	if !ok || !n.Equal(utc(2104, 2, 29, 0, 0)) {
		t.Fatalf("got %v %v, want 2104-02-29 (2100 is not a leap year)", n, ok)
	}
}

// Spring forward, New York, 2026-03-08: 02:00-03:00 does not exist. A slot in
// that hour runs once, at the first valid time after it (03:00 EDT = 07:00Z).
func TestDSTSpringForwardRunsOnce(t *testing.T) {
	s := mustSchedule(t, "30 2 * * *", "America/New_York")
	n1, _ := s.Next(utc(2026, 3, 7, 12, 0))
	if !n1.Equal(utc(2026, 3, 8, 7, 0)) {
		t.Fatalf("spring-forward slot = %v, want 2026-03-08T07:00Z (03:00 EDT)", n1.UTC())
	}
	n2, _ := s.Next(n1)
	if !n2.Equal(utc(2026, 3, 9, 6, 30)) {
		t.Fatalf("day after = %v, want 2026-03-09T06:30Z (02:30 EDT)", n2.UTC())
	}
	// The day before is a normal 02:30 EST.
	n0, _ := s.Next(utc(2026, 3, 6, 12, 0))
	if !n0.Equal(utc(2026, 3, 7, 7, 30)) {
		t.Fatalf("day before = %v, want 2026-03-07T07:30Z", n0.UTC())
	}
}

// Several slots inside the gap collapse into the one first valid instant.
func TestDSTSpringForwardManySlotsCollapse(t *testing.T) {
	s := mustSchedule(t, "0,30 2 * * *", "America/New_York")
	n1, _ := s.Next(utc(2026, 3, 7, 12, 0))
	n2, _ := s.Next(n1)
	if !n1.Equal(utc(2026, 3, 8, 7, 0)) || !n2.Equal(utc(2026, 3, 9, 6, 0)) {
		t.Fatalf("got %v then %v", n1.UTC(), n2.UTC())
	}
	// A dense schedule across the gap: 01:45 EST then 03:00 EDT, 15 minutes apart.
	d := mustSchedule(t, "*/15 * * * *", "America/New_York")
	a, _ := d.Next(utc(2026, 3, 8, 6, 30)) // 01:30 EST
	b, _ := d.Next(a)
	c, _ := d.Next(b)
	if !a.Equal(utc(2026, 3, 8, 6, 45)) || !b.Equal(utc(2026, 3, 8, 7, 0)) || !c.Equal(utc(2026, 3, 8, 7, 15)) {
		t.Fatalf("dense across gap: %v %v %v", a.UTC(), b.UTC(), c.UTC())
	}
	if err := d.Validate(utc(2026, 1, 1, 0, 0)); err != nil {
		t.Fatalf("15-minute schedule across DST: %v", err)
	}
}

// Fall back, New York, 2026-11-01: 01:00-02:00 happens twice. A slot in that
// hour fires once, at its first occurrence (EDT).
func TestDSTFallBackRunsOnce(t *testing.T) {
	s := mustSchedule(t, "30 1 * * *", "America/New_York")
	n1, _ := s.Next(utc(2026, 10, 30, 12, 0))
	if !n1.Equal(utc(2026, 10, 31, 5, 30)) {
		t.Fatalf("day before = %v", n1.UTC())
	}
	n2, _ := s.Next(n1)
	if !n2.Equal(utc(2026, 11, 1, 5, 30)) {
		t.Fatalf("fall-back slot = %v, want first occurrence 2026-11-01T05:30Z (01:30 EDT)", n2.UTC())
	}
	n3, _ := s.Next(n2)
	if !n3.Equal(utc(2026, 11, 2, 6, 30)) {
		t.Fatalf("after fall-back = %v, want 2026-11-02T06:30Z (01:30 EST), not the second 01:30 of 1 Nov", n3.UTC())
	}
}

func TestDSTOtherZone(t *testing.T) {
	// Europe/London springs forward 2026-03-29 at 01:00 GMT; 01:30 does not exist.
	s := mustSchedule(t, "30 1 * * *", "Europe/London")
	n, _ := s.Next(utc(2026, 3, 28, 12, 0))
	if !n.Equal(utc(2026, 3, 29, 1, 0)) {
		t.Fatalf("London gap slot = %v, want 2026-03-29T01:00Z (02:00 BST)", n.UTC())
	}
	// A zone without DST is unaffected.
	k := mustSchedule(t, "30 1 * * *", "Asia/Kolkata")
	kn, _ := k.Next(utc(2026, 3, 28, 12, 0))
	if !kn.Equal(utc(2026, 3, 28, 20, 0)) {
		t.Fatalf("Kolkata = %v", kn.UTC())
	}
}

// Every slot a schedule yields over a year is strictly increasing, whatever
// the zone does.
func TestSlotsStrictlyIncreasingAcrossYear(t *testing.T) {
	for _, tz := range []string{"America/New_York", "Europe/London", "Australia/Sydney", "UTC"} {
		s := mustSchedule(t, "*/20 * * * *", tz)
		cur := utc(2026, 1, 1, 0, 0)
		end := cur.AddDate(1, 0, 0)
		for {
			n, ok := s.Next(cur)
			if !ok {
				t.Fatalf("%s: ran out of slots", tz)
			}
			if !n.After(cur) {
				t.Fatalf("%s: %v is not after %v", tz, n, cur)
			}
			if n.After(end) {
				break
			}
			cur = n
		}
	}
}
