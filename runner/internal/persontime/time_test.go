package persontime

import (
	"strings"
	"testing"
	"time"
)

func TestLocalTimeDST(t *testing.T) {
	a := Account{Timezone: "America/New_York"}
	for _, tc := range []struct{ utc, want string }{
		{"2026-11-01T05:59:00Z", "Person's local time: Sun 2026-11-01 01:59 EDT (America/New_York)"},
		{"2026-11-01T06:00:00Z", "Person's local time: Sun 2026-11-01 01:00 EST (America/New_York)"},
		{"2026-03-08T06:59:00Z", "Person's local time: Sun 2026-03-08 01:59 EST (America/New_York)"},
		{"2026-03-08T07:00:00Z", "Person's local time: Sun 2026-03-08 03:00 EDT (America/New_York)"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.utc)
		if got := a.Line(now); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
	if got := (Account{}).Line(time.Now()); got != "" {
		t.Errorf("unset zone guessed a clock: %q", got)
	}
}

func TestQuietHoursDSTAndBoundaries(t *testing.T) {
	a := Account{Timezone: "America/New_York", QuietHours: &Window{Enabled: true, Start: "22:00", End: "07:00"}}
	for _, tc := range []struct {
		utc  string
		want bool
	}{
		{"2026-11-01T01:59:00Z", false},
		{"2026-11-01T02:00:00Z", true},
		{"2026-11-01T05:30:00Z", true},
		{"2026-11-01T06:30:00Z", true},
		{"2026-11-01T11:59:00Z", true},
		{"2026-11-01T12:00:00Z", false},
		{"2026-03-08T06:30:00Z", true},
		{"2026-03-08T07:30:00Z", true},
	} {
		now, _ := time.Parse(time.RFC3339, tc.utc)
		if got := a.InQuietHours(now); got != tc.want {
			t.Errorf("%s got %v want %v", tc.utc, got, tc.want)
		}
	}
	a.QuietHours = &Window{Enabled: true, Start: "09:00", End: "17:00"}
	for _, hour := range []int{9, 12, 16} {
		if !a.InQuietHours(time.Date(2026, 11, 1, hour+5, 0, 0, 0, time.UTC)) {
			t.Errorf("daytime %d", hour)
		}
	}
	a.QuietHours.Enabled = false
	if a.InQuietHours(time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)) {
		t.Fatal("disabled window")
	}
	if (Account{Timezone: "America/New_York"}).InQuietHours(time.Now()) {
		t.Fatal("default must be off")
	}
}

func TestQuietHoursContextHintAndValidation(t *testing.T) {
	a := Account{Timezone: "America/New_York", QuietHours: &Window{Enabled: true, Start: "01:15", End: "01:45"}}
	for _, utc := range []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"} {
		now, _ := time.Parse(time.RFC3339, utc)
		if !strings.Contains(a.Line(now), "quiet hours; person likely away") {
			t.Error("missing hint in repeated hour", utc)
		}
	}
	for _, w := range []Window{{true, "1:00", "08:00"}, {true, "22:00", "25:00"}, {true, "22:00", "22:00"}, {true, "+1:00", "08:00"}} {
		a.QuietHours = &w
		if a.Valid() {
			t.Errorf("invalid window accepted: %+v", w)
		}
	}
}

func TestQuietHoursLeaseEndAcrossDST(t *testing.T) {
	a := Account{Timezone: "America/New_York", QuietHours: &Window{Enabled: true, Start: "22:00", End: "07:00"}}
	for _, utc := range []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"} {
		now, _ := time.Parse(time.RFC3339, utc)
		want, _ := time.Parse(time.RFC3339, "2026-11-01T12:00:00Z")
		if got := a.QuietEnd(now); !got.Equal(want) {
			t.Fatalf("%s quiet end=%v", utc, got)
		}
	}
	a.QuietHours.End = "02:30"
	now, _ := time.Parse(time.RFC3339, "2026-03-08T06:00:00Z")
	want, _ := time.Parse(time.RFC3339, "2026-03-08T07:00:00Z")
	if got := a.QuietEnd(now); !got.Equal(want) {
		t.Fatalf("missing endpoint should end at DST jump: %v", got)
	}
	if ValidZone("EST") || ValidZone("CET") {
		t.Fatal("fixed-offset abbreviation accepted as a location")
	}
}
