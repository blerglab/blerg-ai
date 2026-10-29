package daemon

import (
	"testing"
	"time"
)

func TestClaudeRateLimitReset(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no tz database")
	}
	now := time.Date(2026, 10, 16, 13, 10, 0, 0, time.UTC)
	for _, c := range []struct {
		text string
		want time.Time // zero: no reset
	}{
		{"Claude AI usage limit reached|1792170000", time.Unix(1792170000, 0)},
		{"5-hour limit reached ∙ resets 3pm", time.Date(2026, 10, 16, 15, 1, 0, 0, time.UTC)},
		{"You've hit your limit · resets 3pm (America/Los_Angeles)", time.Date(2026, 10, 16, 15, 1, 0, 0, la)},
		{"Session limit reached ∙ resets 1:05pm", time.Date(2026, 10, 16, 13, 6, 0, 0, time.UTC)},
		{"limit reached, resets 14:00", time.Date(2026, 10, 16, 14, 1, 0, 0, time.UTC)},
		{"Weekly limit reached ∙ resets Oct 20, 3pm", time.Date(2026, 10, 20, 15, 1, 0, 0, time.UTC)},
		{"Weekly limit reached ∙ resets Oct 22 at 9am", time.Date(2026, 10, 22, 9, 1, 0, 0, time.UTC)},
		{"Weekly limit reached ∙ resets Jan 2 at 9am", time.Time{}}, // further than any window
		{"API Error: Rate limit reached", time.Time{}},
		{"Claude AI usage limit reached|1700000000", time.Time{}}, // in the past
		{"resets 25pm", time.Time{}},
		{"resets 3pm (Not/AZone)", time.Date(2026, 10, 16, 15, 1, 0, 0, time.UTC)},
	} {
		got, ok := claudeRateLimitReset(c.text, now)
		if c.want.IsZero() {
			if ok {
				t.Errorf("%q: reset %v, want none", c.text, got)
			}
			continue
		}
		if !ok || !got.Equal(c.want) {
			t.Errorf("%q: reset %v (%v), want %v", c.text, got, ok, c.want)
		}
	}
	// Across the new year.
	if got, ok := claudeRateLimitReset("resets Jan 2, 9am", time.Date(2026, 12, 30, 8, 0, 0, 0, time.UTC)); !ok || !got.Equal(time.Date(2027, 1, 2, 9, 1, 0, 0, time.UTC)) {
		t.Errorf("across the new year: %v (%v)", got, ok)
	}
}

// A rate_limit API error line that names its reset tells the session's
// tracker; an API error that is not a limit, or names no reset, does not.
func TestClaudeDriverReportsARateLimitWithItsReset(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.now = func() time.Time { return time.Unix(1_792_160_000, 0) }
	d.steer.legacy = false
	p := &ccProc{}
	d.steer.proc = p
	var got []time.Time
	d.onRateLimit = func(reset time.Time) { got = append(got, reset) }
	for _, line := range []string{
		`{"type":"assistant","parent_tool_use_id":null,"error":"rate_limit","message":{"id":"m1","content":[{"type":"text","text":"Claude AI usage limit reached|1792170000"}]}}`,
		`{"type":"assistant","parent_tool_use_id":null,"error":"rate_limit","message":{"id":"m2","content":[{"type":"text","text":"API Error: Rate limit reached"}]}}`,
		`{"type":"assistant","parent_tool_use_id":null,"error":"server_error","message":{"id":"m3","content":[{"type":"text","text":"resets 3pm"}]}}`,
	} {
		d.onLine(p, []byte(line))
	}
	if len(got) != 1 || !got[0].Equal(time.Unix(1792170000, 0)) {
		t.Fatalf("rate limits reported = %v", got)
	}
	// A refusal that comes only on the result line caps too (a turn is under way).
	p.sawInit = true
	d.onLine(p, []byte(`{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached|1792171000"}`))
	if len(got) != 2 || !got[1].Equal(time.Unix(1792171000, 0)) {
		t.Fatalf("rate limits reported after a result line = %v", got)
	}
}
