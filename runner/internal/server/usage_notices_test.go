package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestUsageNoticesCrossingsAndReset(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.usageNotices = true
	ctx := context.Background()
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET engine='claude' WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	child := fx.childOf(fx.parent)
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET engine='claude' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 20}}})
	reset := time.Now().UTC().Add(time.Hour)
	report := func(pct float64) {
		fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: pct, ResetsAt: reset}}})
		fx.api.noteUsageLimits(ctx, fx.dc, "")
	}
	for _, pct := range []float64{79, 80, 80, 89, 90, 90, 95, 99} {
		report(pct)
	}
	var notes []protocol.AgentUserMessage
	for len(fx.dc.send) > 0 {
		var n protocol.AgentUserMessage
		_ = json.Unmarshal(<-fx.dc.send, &n)
		if n.Source == usageNoticeSource {
			notes = append(notes, n)
		}
	}
	if len(notes) != 3 {
		t.Fatalf("got %d notices, want three: %+v", len(notes), notes)
	}
	for i, n := range notes {
		if n.SessionID != fx.parent || !strings.Contains(n.Text, []string{"80%", "90%", "95%"}[i]) || !strings.Contains(n.Text, "codex 7d: 80% headroom") || !strings.Contains(n.Text, "1 running child session on claude") || !strings.Contains(n.Text, reset.Format(time.RFC3339)) {
			t.Fatalf("notice: %+v", n)
		}
	}
	reset = reset.Add(5 * time.Hour)
	report(80)
	if n := fx.parentNote(fx.parent, time.Second); n.Source != usageNoticeSource {
		t.Fatalf("reset did not re-arm: %+v", n)
	}
	if !reservedMessageSource(usageNoticeSource) || answersAsk(usageNoticeSource) {
		t.Fatal("usage source is not protected")
	}
	if !reservedClientID("usage-notice-example") {
		t.Fatal("usage ids are not reserved")
	}
}

func TestUsageNoticesWorkingOnlyAndCatchUp(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.usageNotices = true
	ctx := context.Background()
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET engine='claude', status='idle' WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 96, ResetsAt: time.Now().Add(time.Hour)}}})
	fx.api.noteUsageLimits(ctx, fx.dc, "")
	if len(fx.dc.send) > 0 {
		t.Fatal("idle session got a notice")
	}
	if err := HandleSessionStateChanged(ctx, fx.hub, fx.pool, protocol.SessionStateChanged{SessionID: fx.parent, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	n := fx.parentNote(fx.parent, 5*time.Second)
	if n.Source != usageNoticeSource || !strings.Contains(n.Text, "96%") {
		t.Fatalf("turn-start catch-up: %+v", n)
	}
	fx.api.noteUsageLimits(ctx, fx.dc, fx.parent)
	if len(fx.dc.send) > 0 {
		t.Fatal("catch-up repeated lower thresholds")
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status='idle' WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	fx.childOf(fx.parent)
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 80, ResetsAt: time.Now().Add(time.Hour)}}})
	fx.api.noteUsageLimits(ctx, fx.dc, "")
	if n := fx.parentNote(fx.parent, time.Second); n.Source != usageNoticeSource {
		t.Fatalf("working children not counted: %+v", n)
	}
}

func TestUsageNoticeSettings(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.usageNotices = true
	call := func(method, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/settings/usage-notices", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		fx.api.HandleUsageNoticeSettings(w, req)
		return w
	}
	if w := call(http.MethodPut, `{"thresholds":[70,85,97]}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized settings: %d", w.Code)
	}
	for _, body := range []string{`{}`, `{"thresholds":[90,80,95]}`, `{"thresholds":[0,90,95]}`, `{"thresholds":[80,90,101]}`, `{"thresholds":[80,80,95]}`, `{"thresholds":[80,90]}`, `{"thresholds":[80.5,90,95]}`} {
		if w := call(http.MethodPut, body, delegDaemonToken); w.Code < 400 {
			t.Fatalf("accepted %s: %d", body, w.Code)
		}
	}
	if w := call(http.MethodPut, `{"thresholds":[70,85,97]}`, delegDaemonToken); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if got, err := db.GetSettings(t.Context(), fx.pool); err != nil || got["usage_notice_warning"] != 70 {
		t.Fatalf("settings: %+v %v", got, err)
	}
	if w := call(http.MethodGet, "", delegDaemonToken); w.Code != 200 || !strings.Contains(w.Body.String(), `[70,85,97]`) {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
}

func TestUsageNoticesExcludeNonworkingAndChildSessions(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.usageNotices = true
	ctx := context.Background()
	working := []string{fx.parent}
	second, _ := fx.session(sessOpts{owner: "owner-1"})
	working = append(working, second)
	idle, _ := fx.session(sessOpts{owner: "owner-1", status: "idle"})
	waiting, _ := fx.session(sessOpts{owner: "owner-1", status: "waiting"})
	ended, _ := fx.session(sessOpts{owner: "owner-1", status: "ended"})
	child := fx.childOf(fx.parent)
	orphan, _ := fx.session(sessOpts{owner: "owner-1", startedByKind: "session"})
	other, _ := fx.session(sessOpts{owner: "owner-1"})
	terminal, _ := fx.session(sessOpts{owner: "owner-1"})
	subagentOnly, _ := fx.session(sessOpts{owner: "owner-1", status: "idle"})
	ids := append(append([]string{}, working...), idle, waiting, ended, child, orphan, other, terminal, subagentOnly)
	for _, id := range ids {
		fx.hub.SetSessionOwner(id, fx.dc.ID)
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET engine='claude' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET engine='codex' WHERE id=$1`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET kind='tmux' WHERE id=$1`, terminal); err != nil {
		t.Fatal(err)
	}
	fx.childEvent(subagentOnly, "", "subagent_started", `{"child_id":"native-review","summary":"Review","background":true}`)
	candidates, err := db.UsageNoticeCandidates(ctx, fx.pool, fx.dc.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{idle, waiting, ended, child, orphan, terminal, subagentOnly} {
		for _, candidate := range candidates {
			if candidate == excluded {
				t.Fatalf("SQL returned nonworking/child candidate %s", excluded)
			}
		}
	}
	// A jump through several thresholds produces one summary for each working parent.
	report := protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 96, ResetsAt: time.Now().Add(time.Hour)}}}
	fx.dc.SetUsageLimits(report)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { fx.api.noteUsageLimits(ctx, fx.dc, "") })
	}
	wg.Wait()
	counts := map[string]int{}
	for len(fx.dc.send) > 0 {
		var n protocol.AgentUserMessage
		_ = json.Unmarshal(<-fx.dc.send, &n)
		if n.Source == usageNoticeSource {
			counts[n.SessionID]++
		}
	}
	if len(counts) != 2 || counts[working[0]] != 1 || counts[working[1]] != 1 {
		t.Fatalf("recipients: %+v", counts)
	}
	// A fresh API (server restart) reads the delivered thresholds from the database.
	restarted := NewAPI(fx.hub, fx.pool, delegDaemonToken, nil, "")
	restarted.noteUsageLimits(ctx, fx.dc, "")
	if len(fx.dc.send) > 0 {
		t.Fatal("restart repeated delivered notices")
	}
}

func TestUsageNoticesReportHookAndConfiguredThreshold(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.usageNotices = true
	fx.dc.SetInputReceipts(true)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine='codex' WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	for i, k := range usageNoticeSettingKeys {
		if err := db.SetSetting(t.Context(), fx.pool, k, int64([]int{70, 85, 97}[i]), "test"); err != nil {
			t.Fatal(err)
		}
	}
	reset := time.Now().Add(time.Hour)
	// Exercise the actual report hook rather than the notifier alone.
	handleUsageLimits(fx.hub, fx.dc, protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 70, ResetsAt: reset}}})
	n := fx.parentNote(fx.parent, 5*time.Second)
	if n.Source != usageNoticeSource || !strings.HasPrefix(n.ClientID, "usage-notice-") || !strings.Contains(n.Text, "threshold 70%") || !strings.Contains(n.Text, "Prefer other engines") {
		t.Fatalf("configured report: %+v", n)
	}
	// Dropping below the lowest configured threshold starts a new instance, even
	// when the provider repeats the same reset timestamp.
	for _, pct := range []float64{10, 79} {
		fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: pct, ResetsAt: reset}}})
		fx.api.noteUsageLimits(t.Context(), fx.dc, "")
	}
	if n := fx.parentNote(fx.parent, time.Second); !strings.Contains(n.Text, "threshold 70%") {
		t.Fatalf("low reading did not re-arm configured threshold: %+v", n)
	}
}

func TestUsageNoticesRequireDaemonCapability(t *testing.T) {
	fx := newDelegFx(t) // Old daemons have no usage_notices hello flag.
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 96}}})
	for _, session := range []string{"", fx.parent} {
		fx.api.noteUsageLimits(t.Context(), fx.dc, session)
	}
	if len(fx.dc.send) != 0 {
		t.Fatal("old daemon received usage notices in the person's voice")
	}
	fx.dc.usageNotices = true
	fx.api.noteUsageLimits(t.Context(), fx.dc, fx.parent)
	if n := fx.parentNote(fx.parent, time.Second); n.Source != usageNoticeSource {
		t.Fatalf("capable daemon missed catch-up: %+v", n)
	}
}

func TestUsageNoticesIgnoreCappedWindow(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.usageNotices = true
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	for _, reset := range []time.Time{{}, time.Now().Add(time.Hour), time.Now().Add(2 * time.Hour)} {
		fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "capped", UsedPercent: 100, ResetsAt: reset}}})
		fx.api.noteUsageLimits(t.Context(), fx.dc, "")
	}
	if len(fx.dc.send) != 0 {
		t.Fatal("synthetic refusal window generated usage notices")
	}
}

func TestUsageNoticesWindowInstances(t *testing.T) {
	for _, window := range []struct {
		id        string
		tolerance time.Duration
	}{{"5h", time.Hour}, {"7d", 12 * time.Hour}, {"7d_opus", 12 * time.Hour}, {"7d_sonnet", 12 * time.Hour}, {"7d_fable", 12 * time.Hour}} {
		t.Run(window.id, func(t *testing.T) {
			fx := newDelegFx(t)
			fx.dc.usageNotices = true
			fx.dc.SetInputReceipts(true)
			fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
			reset := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
			report := func(pct float64, at time.Time) {
				fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: window.id, UsedPercent: pct, ResetsAt: at}}})
				fx.api.noteUsageLimits(t.Context(), fx.dc, "")
			}
			report(96, reset)
			first := fx.parentNote(fx.parent, time.Second)
			if !strings.Contains(first.Text, "threshold 95%") || !strings.Contains(first.Text, "Also crossed 80%, 90%") || !strings.Contains(first.Text, "Finish the current step") || len(fx.dc.send) != 0 {
				t.Fatalf("jump must produce one highest-level summary: %+v", first)
			}
			for _, at := range []time.Time{reset.Add(time.Nanosecond), reset.Add(-time.Minute), reset.Add(window.tolerance), reset, reset} {
				report(96, at)
			}
			if len(fx.dc.send) != 0 {
				t.Fatal("reset jitter or repeated wrong reset resent a notice")
			}
			// A substantial forward move re-arms once, with a new receipt ID.
			reset = reset.Add(window.tolerance + time.Minute)
			report(96, reset)
			second := fx.parentNote(fx.parent, time.Second)
			if second.ClientID == first.ClientID {
				t.Fatal("new instance reused input receipt")
			}
			report(96, reset)
			if len(fx.dc.send) != 0 {
				t.Fatal("new reset repeated its notice")
			}
			// A drop within the warned band does not re-arm.
			report(85, reset)
			report(96, reset)
			if len(fx.dc.send) != 0 {
				t.Fatal("above-floor fluctuation re-armed")
			}
			// Observe the low reading even while the parent is idle. Unknown reset
			// values must also re-arm when usage drops below the floor.
			if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='idle' WHERE id=$1`, fx.parent); err != nil {
				t.Fatal(err)
			}
			report(10, time.Time{})
			if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='running' WHERE id=$1`, fx.parent); err != nil {
				t.Fatal(err)
			}
			report(96, time.Time{})
			third := fx.parentNote(fx.parent, time.Second)
			if third.ClientID == second.ClientID {
				t.Fatal("usage drop reused previous instance")
			}
			report(10, time.Time{})
			report(96, time.Time{})
			fourth := fx.parentNote(fx.parent, time.Second)
			if fourth.ClientID == third.ClientID {
				t.Fatal("unknown reset did not re-arm after low usage")
			}
		})
	}
}

func TestUsageNoticePercentFormatting(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.usageNotices = true
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "codex", Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 87.3}}})
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 95.123456789}}})
	fx.api.noteUsageLimits(t.Context(), fx.dc, "")
	n := fx.parentNote(fx.parent, time.Second)
	if !strings.Contains(n.Text, "95.1% used") || !strings.Contains(n.Text, "12.7% headroom") {
		t.Fatalf("float noise in notice: %s", n.Text)
	}
}

func TestUsageNoticeRetention(t *testing.T) {
	fx := newDelegFx(t)
	for _, window := range []string{"5h", "7d"} {
		if _, _, err := db.UsageNoticeState(t.Context(), fx.pool, fx.parent, fx.dc.ID, "claude", window, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE session_usage_notices SET updated_at=now()-interval '36 days' WHERE window_id='5h'`); err != nil {
		t.Fatal(err)
	}
	_, instance, err := db.UsageNoticeState(t.Context(), fx.pool, fx.parent, fx.dc.ID, "claude", "month", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetUsageNoticeThreshold(t.Context(), fx.pool, fx.parent, fx.dc.ID, "claude", "month", instance, 95); err != nil {
		t.Fatal(err)
	}
	ended, _ := fx.session(sessOpts{owner: "owner-1", status: "ended"})
	_, endedInstance, err := db.UsageNoticeState(t.Context(), fx.pool, ended, fx.dc.ID, "claude", "month", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetUsageNoticeThreshold(t.Context(), fx.pool, ended, fx.dc.ID, "claude", "month", endedInstance, 95); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE session_usage_notices SET updated_at=now()-interval '36 days' WHERE window_id='month'`); err != nil {
		t.Fatal(err)
	}
	// A routine usage report (no re-arm) must not refresh the rows' age, or an active desktop
	// never prunes its ended sessions' state.
	if err := db.ObserveUsageNoticeWindow(t.Context(), fx.pool, fx.dc.ID, "claude", "month", time.Time{}, false, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneUsageNotices(t.Context(), fx.pool, fx.dc.ID); err != nil {
		t.Fatal(err)
	}
	var old, fresh int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE window_id='5h'),count(*) FILTER(WHERE window_id='7d') FROM session_usage_notices`).Scan(&old, &fresh); err != nil {
		t.Fatal(err)
	}
	var liveWarned, endedWarned int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE session_id=$1),count(*) FILTER(WHERE session_id=$2) FROM session_usage_notices WHERE window_id='month'`, fx.parent, ended).Scan(&liveWarned, &endedWarned); err != nil {
		t.Fatal(err)
	}
	if liveWarned != 1 || endedWarned != 0 {
		t.Fatalf("retention lost live dedup state or kept ended state: live=%d ended=%d", liveWarned, endedWarned)
	}
	if old != 0 || fresh != 1 {
		t.Fatalf("retention removed wrong instances: old=%d fresh=%d", old, fresh)
	}
}

func TestUsageNoticeTimeoutStartsAfterLock(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.usageNotices = true
	fx.api.usageNoticeTimeout = time.Second
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 80}}})
	fx.api.usageNoticesMu.Lock()
	fx.api.onUsageNotices(fx.dc, fx.parent)
	// A queued check may wait longer than its delivery budget. Its fresh budget
	// must begin when it can actually query and send, not when it was queued.
	time.Sleep(2 * fx.api.usageNoticeTimeout)
	fx.api.usageNoticesMu.Unlock()
	if n := fx.parentNote(fx.parent, 5*time.Second); n.Source != usageNoticeSource {
		t.Fatalf("lock wait exhausted delivery budget: %+v", n)
	}
	// Join delivery and verify its state was persisted before the fixture closes.
	fx.api.usageNoticesMu.Lock()
	previous, _, err := db.UsageNoticeState(t.Context(), fx.pool, fx.parent, fx.dc.ID, "claude", "5h", time.Time{})
	fx.api.usageNoticesMu.Unlock()
	if err != nil || previous != 80 {
		t.Fatalf("delivery state was not persisted: threshold=%d err=%v", previous, err)
	}
}

func TestUsageNoticesIgnoreReplacedConnection(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.usageNotices = true
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 96}}})
	fx.api.noteUsageLimits(t.Context(), fx.dc, "")
	if n := fx.parentNote(fx.parent, time.Second); n.Source != usageNoticeSource {
		t.Fatalf("initial notice: %+v", n)
	}
	// A queued callback from a superseded connection can still have the same
	// daemon ID. Its low reading must not clear the current connection's state.
	stale := &DaemonConn{ID: fx.dc.ID, Mode: fx.dc.Mode, usageNotices: true}
	stale.SetUsageLimits(protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{{ID: "5h", UsedPercent: 10}}})
	fx.api.noteUsageLimits(t.Context(), stale, "")
	fx.api.noteUsageLimits(t.Context(), fx.dc, "")
	if len(fx.dc.send) > 0 {
		t.Fatal("stale daemon connection re-armed the current window")
	}
}
