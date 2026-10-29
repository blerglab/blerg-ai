package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestLimitResumeRetryResetEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		resetOffset                   time.Duration
		pct                           float64
		meter, wantUnknown, wantMeter bool
	}{
		{"future_text_uncapped_meter", 48 * time.Hour, 60, true, false, false},
		{"future_text_rounded_99_meter", 48 * time.Hour, 99, true, false, false},
		{"future_text_fractional_99_meter", 48 * time.Hour, 99.6, true, false, false},
		{"future_text_capped_meter", 48 * time.Hour, 100, true, false, true},
		{"just_passed_text_no_meter", -2 * time.Minute, 0, false, true, false},
		{"just_passed_after_dispatch_no_meter", -2 * time.Minute, 0, false, true, false},
		{"just_passed_text_uncapped_meter", -2 * time.Minute, 60, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIngestFixture(t)
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.pool.Exec(f.ctx, `UPDATE sessions SET kind='agent',engine='claude' WHERE id=$1`, f.sessionID)
			check(err)
			f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
			check(db.SetLimitResume(f.ctx, f.pool, f.sessionID, true, newUUID()))
			at := time.Now().Add(-time.Second)
			f.ingest("turn", "user_message", `{"turn_id":"retry","source":"limit_resume"}`, at.Format(time.RFC3339Nano))
			_, err = f.pool.Exec(f.ctx, `UPDATE session_limit_resume SET attempts=1,dispatched_at=now()-interval '30 seconds' WHERE session_id=$1`, f.sessionID)
			check(err)
			if tc.name == "just_passed_after_dispatch_no_meter" {
				_, err = f.pool.Exec(f.ctx, `UPDATE session_limit_resume SET dispatched_at=now()-interval '3 minutes' WHERE session_id=$1`, f.sessionID)
				check(err)
			}
			meterReset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			if tc.meter {
				handleUsageLimits(f.hub, f.dc, protocol.UsageLimits{Provider: "claude", FetchedAt: at.Add(-time.Second), Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: tc.pct, ResetsAt: meterReset}}})
			}
			textReset := time.Now().Add(tc.resetOffset).UTC().Truncate(time.Second)
			raw, err := json.Marshal(limitRefusal{Provider: "claude", TurnID: "retry", WindowID: "7d", ResetAt: &textReset})
			check(err)
			f.ingest("refusal", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
			state, err := db.GetLimitResume(f.ctx, f.pool, f.sessionID)
			check(err)
			if tc.wantUnknown {
				if state.State != "unknown" || state.ResetAt != nil {
					t.Fatalf("already-due retry: %+v", state)
				}
				f.acks()
				NewAPI(f.hub, f.pool, "", nil, "").checkLimitResumes(f.ctx)
				state, err = db.GetLimitResume(f.ctx, f.pool, f.sessionID)
				check(err)
				if state.Attempts != 1 || state.State != "unknown" {
					t.Fatalf("spent retry without fresh evidence: %+v", state)
				}
			} else {
				want := textReset
				if tc.wantMeter {
					want = meterReset
				}
				if state.State != "pending" || state.ResetAt == nil || !state.ResetAt.Equal(want) {
					t.Fatalf("reset=%v state=%s, want %v", state.ResetAt, state.State, want)
				}
			}
		})
	}
}

func TestLimitResumeModelMeterIsolationAndRoundedCap(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(2 * time.Hour)
	for _, window := range []string{"7d_opus", "7d_sonnet", "7d_fable"} {
		t.Run(window, func(t *testing.T) {
			dc := &DaemonConn{}
			dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", FetchedAt: now, Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 40, ResetsAt: reset}}})
			if got := limitResumeMeterReset(dc, "claude", window, now.Add(-time.Minute)); got != nil {
				t.Fatalf("borrowed general weekly: %v", got)
			}
			u, ok := sanitizeUsageLimits(protocol.UsageLimits{Provider: "claude", FetchedAt: now, Windows: []protocol.UsageWindow{{ID: window, UsedPercent: 100, ResetsAt: reset}}}, now)
			if !ok {
				t.Fatal("provider rejected")
			}
			dc.SetUsageLimits(u)
			if got := limitResumeMeterReset(dc, "claude", window, now.Add(-time.Minute)); got == nil || !got.Equal(reset) {
				t.Fatalf("matching cap ignored: %v", got)
			}
		})
	}
	for _, pct := range []float64{99, 99.6} {
		dc := &DaemonConn{}
		dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", FetchedAt: now, Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: pct, ResetsAt: reset}}})
		if got := limitResumeMeterReset(dc, "claude", "7d", now.Add(-time.Minute)); got != nil {
			t.Errorf("near-cap %.1f authorized retry: %v", pct, got)
		}
	}
}

func TestLimitResumeUnknownPollingBackoffAndCutoff(t *testing.T) {
	f := newIngestFixture(t)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.pool.Exec(f.ctx, `UPDATE sessions SET kind='agent',engine='claude' WHERE id=$1`, f.sessionID)
	check(err)
	f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
	check(db.SetLimitResume(f.ctx, f.pool, f.sessionID, true, newUUID()))
	at := time.Now().Add(-time.Second)
	f.ingest("turn", "user_message", `{"turn_id":"turn","source":"chat"}`, at.Format(time.RFC3339Nano))
	f.ingest("refusal", "usage_limit", `{"provider":"claude","turn_id":"turn","window_id":"7d_opus"}`, at.Format(time.RFC3339Nano))
	f.acks()
	api := NewAPI(f.hub, f.pool, "", nil, "")
	api.checkLimitResumes(f.ctx)
	select {
	case <-f.dc.send:
	default:
		t.Fatal("no initial meter poll")
	}
	NewAPI(f.hub, f.pool, "", nil, "").checkLimitResumes(f.ctx)
	select {
	case raw := <-f.dc.send:
		t.Errorf("polled again within seconds: %s", raw)
	default:
	}

	// Older unknown waits must make fewer provider reads while remaining durable.
	for _, age := range []time.Duration{2 * time.Hour, 8 * time.Hour, 2 * 24 * time.Hour} {
		_, err = f.pool.Exec(f.ctx, `UPDATE session_limit_resume SET refused_at=now()-$2*interval '1 second',next_meter_poll_at=now()-interval '1 second' WHERE session_id=$1`, f.sessionID, age.Seconds())
		check(err)
		api.checkLimitResumes(f.ctx)
		select {
		case <-f.dc.send:
		default:
			t.Fatal("backoff never polled again")
		}
		var delay float64
		check(f.pool.QueryRow(f.ctx, `SELECT extract(epoch FROM next_meter_poll_at-now()) FROM session_limit_resume WHERE session_id=$1`, f.sessionID).Scan(&delay))
		minimum := map[time.Duration]float64{2 * time.Hour: 290, 8 * time.Hour: 890, 2 * 24 * time.Hour: 3590}[age]
		if delay < minimum {
			t.Errorf("wait %v: next poll in %vs", age, delay)
		}
	}
	_, err = f.pool.Exec(f.ctx, `UPDATE session_limit_resume SET refused_at=now()-interval '7 days 1 minute',expires_at=now()+interval '23 hours' WHERE session_id=$1`, f.sessionID)
	check(err)
	api.checkLimitResumes(f.ctx)
	state, err := db.GetLimitResume(f.ctx, f.pool, f.sessionID)
	check(err)
	if state.State != "expired" || state.Attempts != 0 {
		t.Fatalf("unknown wait did not stop: %+v", state)
	}
	// A re-refusal cannot restart the seven-day budget from a new timestamp.
	_, err = f.pool.Exec(f.ctx, `UPDATE session_limit_resume SET state='unknown',refused_at=now() WHERE session_id=$1`, f.sessionID)
	check(err)
	api.checkLimitResumes(f.ctx)
	state, err = db.GetLimitResume(f.ctx, f.pool, f.sessionID)
	check(err)
	if state.State != "expired" || state.Attempts != 0 {
		t.Fatalf("re-refusal extended unknown wait: %+v", state)
	}
}
