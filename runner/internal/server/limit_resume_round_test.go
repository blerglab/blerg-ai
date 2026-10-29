package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLimitResumeMeterFallbackAndPolling(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		for _, mode := range []string{"known", "unknown_then_known", "unknown_then_reset", "retry"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				f := newIngestFixture(t)
				ctx := f.ctx
				check := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				_, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent',engine=$2 WHERE id=$1`, f.sessionID, engine)
				check(err)
				f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
				check(db.SetLimitResume(ctx, f.pool, f.sessionID, true, newUUID()))
				at := time.Now().Add(-time.Second)
				f.ingest("turn", "user_message", `{"turn_id":"interrupted","source":"chat"}`, at.Format(time.RFC3339Nano))
				reset := time.Now().Add(3 * 24 * time.Hour).UTC().Truncate(time.Second)
				report := protocol.UsageLimits{Provider: engine, FetchedAt: time.Now(), Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 10, ResetsAt: time.Now().Add(time.Hour)}, {ID: "7d", UsedPercent: 100, ResetsAt: reset}}}
				if mode == "known" || mode == "retry" {
					handleUsageLimits(f.hub, f.dc, report)
				}
				refusal := map[string]any{"provider": engine, "turn_id": "interrupted", "window_id": "7d"}
				if mode == "retry" {
					// A re-refused automatic turn has misleading tomorrow text. The account's
					// weekly meter remains authoritative for this window.
					_, err = f.pool.Exec(ctx, `UPDATE session_limit_resume SET attempts=1,dispatched_at=now() WHERE session_id=$1`, f.sessionID)
					check(err)
					refusal["reset_at"] = time.Now().Add(24 * time.Hour)
				}
				raw, err := json.Marshal(refusal)
				check(err)
				f.ingest("refusal", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
				api := NewAPI(f.hub, f.pool, "", nil, "")
				if mode == "unknown_then_known" || mode == "unknown_then_reset" {
					state, err := db.GetLimitResume(ctx, f.pool, f.sessionID)
					check(err)
					if state.State != "unknown" {
						t.Fatalf("want waiting for meter: %+v", state)
					}
					f.acks()
					api.checkLimitResumes(ctx)
					select {
					case raw := <-f.dc.send:
						var req struct {
							Type string `json:"type"`
						}
						check(json.Unmarshal(raw, &req))
						if req.Type != "usage_limits_request" {
							t.Fatalf("no meter poll: %s", raw)
						}
					default:
						t.Fatal("unknown reset did not request meter refresh")
					}
					if mode == "unknown_then_reset" {
						report.Windows[1].UsedPercent = 0
					}
					report.FetchedAt = time.Now()
					handleUsageLimits(f.hub, f.dc, report)
					api.checkLimitResumes(ctx)
				}
				state, err := db.GetLimitResume(ctx, f.pool, f.sessionID)
				check(err)
				if state.State != "pending" || state.ResetAt == nil {
					t.Fatalf("meter did not arm: %+v", state)
				}
				if mode != "unknown_then_reset" && !state.ResetAt.Equal(reset) {
					t.Fatalf("wrong window/reset: %v want %v", state.ResetAt, reset)
				}
				if mode == "unknown_then_reset" && state.ResetAt.After(time.Now()) {
					t.Fatal("fresh available window should resume after grace, not at next weekly reset")
				}
				f.acks()
				_, err = f.pool.Exec(ctx, `UPDATE session_limit_resume SET reset_at=now()-interval '3 minutes' WHERE session_id=$1`, f.sessionID)
				check(err)
				api.checkLimitResumes(ctx)
				select {
				case raw := <-f.dc.send:
					var msg protocol.AgentUserMessage
					check(json.Unmarshal(raw, &msg))
					if msg.Source != limitResumeSource || msg.ExpectedTurnID != "interrupted" {
						t.Fatalf("wrong continuation: %s", raw)
					}
				default:
					t.Fatal("meter-backed continuation did not fire")
				}
			})
		}
	}
}

func TestUnknownResetRetainsAutoStopSession(t *testing.T) {
	api, pool, id := autoStopSession(t, true)
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(db.SetLimitResume(ctx, pool, id, true, newUUID()))
	check(db.TrackLimitResumeTurn(ctx, pool, id, "turn", "chat", time.Now(), 1))
	check(db.RecordLimitRefusal(ctx, pool, id, "claude", "turn", nil, 2, time.Now()))
	turnDoneWithReason(t, api, pool, id, "error")
	if got := sessionStatus(t, pool, id); got != "running" {
		t.Fatalf("meter wait ended one-shot: %s", got)
	}
}

func TestLimitResumeMeterScopeAndFreshness(t *testing.T) {
	now := time.Now().UTC()
	five := now.Add(time.Hour)
	weekly := now.Add(3 * 24 * time.Hour)
	for _, tc := range []struct {
		name, window, provider string
		windows                []protocol.UsageWindow
		fetched, after         time.Time
		want                   *time.Time
	}{
		{name: "weekly_not_five_hour", window: "7d", windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 100, ResetsAt: five}, {ID: "7d", UsedPercent: 100, ResetsAt: weekly}}, want: &weekly},
		{name: "both_caps_must_reset", windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 100, ResetsAt: five}, {ID: "7d", UsedPercent: 100, ResetsAt: weekly}}, want: &weekly},
		{name: "wrong_engine", provider: "codex", window: "7d", windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 100, ResetsAt: weekly}}},
		{name: "wrong_window", window: "7d", windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 100, ResetsAt: five}}},
		{name: "withdrawal_is_not_reset", window: "7d", fetched: now, after: now.Add(-time.Minute)},
		{name: "unknown_cap_must_poll", window: "7d", windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 100}}, fetched: now, after: now.Add(-time.Minute)},
		{name: "fresh_available", window: "7d", windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 0}}, fetched: now, after: now.Add(-time.Minute), want: &now},
		{name: "stale_available_cannot_resume", window: "7d", windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 0}}, fetched: now.Add(-time.Hour), after: now.Add(-time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := tc.provider
			if provider == "" {
				provider = "claude"
			}
			dc := &DaemonConn{}
			dc.SetUsageLimits(protocol.UsageLimits{Provider: provider, Windows: tc.windows, FetchedAt: tc.fetched})
			got := limitResumeMeterReset(dc, "claude", tc.window, tc.after)
			if (got == nil) != (tc.want == nil) || (got != nil && !got.Equal(*tc.want)) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestLimitResumeLateOptInPollsUnknownReset(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent',engine='claude' WHERE id=$1`, f.sessionID)
	check(err)
	f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
	at := time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	f.ingest("turn", "user_message", `{"turn_id":"interrupted","source":"chat"}`, at)
	f.ingest("limit", "usage_limit", `{"provider":"claude","turn_id":"interrupted","window_id":"7d"}`, at)
	check(db.SetLimitResume(ctx, f.pool, f.sessionID, true, newUUID()))
	state, err := db.GetLimitResume(ctx, f.pool, f.sessionID)
	check(err)
	if state.State != "unknown" {
		t.Fatalf("late opt-in lost unknown refusal: state=%s", state.State)
	}
	f.acks()
	api := NewAPI(f.hub, f.pool, "", nil, "")
	api.checkLimitResumes(ctx)
	select {
	case raw := <-f.dc.send:
		var req protocol.UsageLimitsRequest
		check(json.Unmarshal(raw, &req))
		if req.Type != "usage_limits_request" {
			t.Fatalf("no poll: %s", raw)
		}
	default:
		t.Fatal("late opt-in did not poll meter")
	}
}

func TestUnknownResetOptOutSettlesIdleAutoStop(t *testing.T) {
	for _, status := range []string{"idle", "running"} {
		t.Run(status, func(t *testing.T) {
			api, pool, id := autoStopSession(t, true)
			ctx := context.Background()
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id='user-1',engine='claude' WHERE id=$1`, id)
			check(err)
			check(db.SetLimitResume(ctx, pool, id, true, newUUID()))
			check(db.TrackLimitResumeTurn(ctx, pool, id, "turn", "chat", time.Now(), 1))
			check(db.RecordLimitRefusal(ctx, pool, id, "claude", "turn", nil, 2, time.Now()))
			check(db.UpdateSessionStatus(ctx, pool, id, status, nil))
			token := enableBrowserAuth(t, api)(coreAuthBrowserCap)
			req := httptest.NewRequest(http.MethodPut, "/api/sessions/"+id+"/limit-resume", strings.NewReader(`{"enabled":false}`))
			req.SetPathValue("id", id)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			api.HandleLimitResume(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
			}
			want := status
			if status == "idle" {
				want = "ended"
			}
			if got := sessionStatus(t, pool, id); got != want {
				t.Fatalf("unknown reset opt-out: got %s want %s", got, want)
			}
		})
	}
}
