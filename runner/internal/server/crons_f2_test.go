package server

// Fix-round tests for the crons start path and API: the renew-versus-claim race, the token
// independent idempotency scope, a core that answers 404, persistent token revocation, the enabled
// toggle, an explicit zero grace and editing a cron whose connection is gone.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/cron"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const cronTokRenewed = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c9"

// swapToken is what a renewal does to the row: a new live token, the old one revoked.
func (cf *cronFx) swapToken(c *db.Cron, to string) {
	cf.t.Helper()
	cf.core.mu.Lock()
	cf.core.live[to] = true
	cf.core.live[c.TokenID] = false
	cf.core.mu.Unlock()
	if _, err := db.UpdateCron(context.Background(), cf.pool, c.OwnerAccountID, c.ID, func(x *db.Cron) error {
		x.TokenID = to
		return nil
	}); err != nil {
		cf.t.Fatal(err)
	}
}

// MAJOR 5: a renewal that lands between the liveness check and the pause must not leave a cron
// with a live new token paused as "access revoked".
func TestRenewBetweenClaimAndLivenessDoesNotPause(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(nil)
	cf.core.onStatus = func() {
		cf.core.mu.Lock()
		cf.core.onStatus = nil
		cf.core.mu.Unlock()
		cf.swapToken(c, cronTokRenewed) // the renewal, mid-check
	}
	_, err := cf.svc.Start(ctx, c, cf.run(c))
	if !isCapacity(err) {
		t.Fatalf("err = %v, want a hold (capacity), not a failure", err)
	}
	got, _ := db.GetCron(ctx, cf.pool, c.ID)
	if got.PausedReason != nil {
		t.Fatalf("cron paused (%q) though it holds a live new token", *got.PausedReason)
	}
	if got.TokenID != cronTokRenewed {
		t.Fatalf("token = %s", got.TokenID)
	}
	cf.assertNothingWritten("renewal race")
	// The next attempt, with the row as it is now, starts.
	if _, err := cf.svc.Start(ctx, got, cf.run(got)); err != nil {
		t.Fatalf("start after the renewal: %v", err)
	}
}

// MAJOR 5: Start works from the cron's current row, not the (possibly stale) one it was handed, and
// the run's idempotency scope is the cron's, so a replay after a renewal finds the same session.
func TestStartUsesTheCurrentTokenAndAStableScope(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(nil)
	stale := *c
	cf.swapToken(c, cronTokRenewed) // renewed after the scheduler read the row
	run := cf.run(c)
	sid, err := cf.svc.Start(ctx, &stale, run)
	if err != nil {
		t.Fatalf("start with a stale row: %v (the old token is revoked, the current one is live)", err)
	}
	if k, _ := db.GetIdempotencyKey(ctx, cf.pool, "cron:"+c.ID, run.IdempotencyKey()); k == nil || k.SessionID != sid {
		t.Fatalf("key row = %+v, want it in scope cron:<cron id>", k)
	}
	if k, _ := db.GetIdempotencyKey(ctx, cf.pool, "agent:"+cronTokRenewed, run.IdempotencyKey()); k != nil {
		t.Fatal("the key was written in a token scoped bucket")
	}
	// Renewed again, the replay of the same run still answers with the same session.
	cur, _ := db.GetCron(ctx, cf.pool, c.ID)
	cf.swapToken(cur, cronTok)
	again, err := cf.svc.Start(ctx, &stale, run)
	if err != nil || again != sid {
		t.Fatalf("replay after another renewal = %q, %v; want %q", again, err, sid)
	}
	if n := cf.count("sessions"); n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
}

func TestStartOfACronThatCannotRunIsNotRunnable(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	paused := cf.newCron(func(x *db.Cron) { x.PausedReason = ptr("paused by you") })
	if _, err := cf.svc.Start(ctx, paused, cf.run(paused)); !errors.Is(err, cron.ErrNotRunnable) {
		t.Errorf("paused: %v", err)
	}
	off := cf.newCron(func(x *db.Cron) { x.TokenID = newUUID(); x.Enabled = false })
	if _, err := cf.svc.Start(ctx, off, cf.run(off)); !errors.Is(err, cron.ErrNotRunnable) {
		t.Errorf("disabled: %v", err)
	}
	gone := *paused
	gone.ID = newUUID()
	if _, err := cf.svc.Start(ctx, &gone, cf.run(&gone)); !errors.Is(err, cron.ErrNotRunnable) {
		t.Errorf("deleted: %v", err)
	}
	cf.assertNothingWritten("not runnable")
}

// MINOR 7: a core that answers 404 to the status call (a proxy, a rolled-back deploy) holds the run;
// it never pauses the cron.
func TestCoreNotFoundOnStatusHoldsTheRunInsteadOfPausing(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer srv.Close()
	svc := NewCronService(cf.api, CronServiceConfig{Core: &HTTPCronCore{BaseURL: srv.URL, InternalKey: "k", HTTP: srv.Client()}, Logf: t.Logf})
	c := cf.newCron(nil)
	_, err := svc.Start(ctx, c, cf.run(c))
	if !isCapacity(err) {
		t.Fatalf("err = %v, want a hold", err)
	}
	if got, _ := db.GetCron(ctx, cf.pool, c.ID); got.PausedReason != nil {
		t.Fatalf("cron paused: %q", *got.PausedReason)
	}
	cf.assertNothingWritten("core 404")
}

// MINOR 8: a token revocation core could not take at pause time is kept and retried until it can.
func TestPauseRevocationSurvivesACoreOutage(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	cf.svc.cfg.Now = cf.clock.Now
	c := cf.newCron(nil)
	cf.core.revokeErr = errors.New("core is down")
	cf.svc.OnPaused(ctx, c, "paused after 3 failed runs in a row")
	owed, err := db.ListTokenRevocations(ctx, cf.pool, 10)
	if err != nil || len(owed) != 1 || owed[0].TokenID != cronTok || owed[0].AccountID != mcpAcct {
		t.Fatalf("owed = %+v, %v; want the paused cron's token remembered", owed, err)
	}
	if !cf.core.live[cronTok] {
		t.Fatal("setup: the token must still be live at core")
	}
	if n := cf.svc.RetryRevocations(ctx); n != 0 {
		t.Fatalf("retry while core is down cleared %d", n)
	}
	if owed, _ := db.ListTokenRevocations(ctx, cf.pool, 10); len(owed) != 1 {
		t.Fatalf("the record was lost while core was down: %+v", owed)
	}
	cf.core.revokeErr = nil
	cf.clock.Advance(time.Hour) // the failed retry backed off
	if n := cf.svc.RetryRevocations(ctx); n != 1 {
		t.Fatalf("retry cleared %d, want 1", n)
	}
	if cf.core.live[cronTok] {
		t.Error("the token is still live after the retry")
	}
	if owed, _ := db.ListTokenRevocations(ctx, cf.pool, 10); len(owed) != 0 {
		t.Fatalf("still owed after the ack: %+v", owed)
	}
}

// The same goes for the old token a renewal retires, over the API.
func TestRenewKeepsTheOldTokenOwedWhenCoreIsDown(t *testing.T) {
	fx := newCronsFx(t)
	ctx := context.Background()
	_, row := fx.create(nil)
	fx.core.revokeErr = errors.New("core is down")
	if rec := fx.call(http.MethodPost, "/api/crons/"+row.ID+"/renew", fx.human(), ""); rec.Code != http.StatusOK {
		t.Fatalf("renew: %d %s", rec.Code, rec.Body.String())
	}
	if owed, _ := db.ListTokenRevocations(ctx, fx.pool, 10); len(owed) != 1 || owed[0].TokenID != row.TokenID {
		t.Fatalf("owed = %+v, want the retired token", owed)
	}
	fx.core.revokeErr = nil
	if n := fx.svc.RetryRevocations(ctx); n != 1 {
		t.Fatalf("retry cleared %d", n)
	}
}

// MAJOR 6: the form's explicit 0 (no catch-up window) is kept on create, and the response says so.
func TestCreateCronHonoursAnExplicitZeroGrace(t *testing.T) {
	fx := newCronsFx(t)
	_, zero := fx.create(func(m map[string]any) { m["grace_seconds"] = 0 })
	if zero.GraceSeconds != 0 {
		t.Fatalf("explicit 0 stored as %d", zero.GraceSeconds)
	}
	_, def := fx.create(func(m map[string]any) { m["name"] = "second" })
	if def.GraceSeconds != cron.DefaultGraceSeconds {
		t.Fatalf("absent grace stored as %d, want the default %d", def.GraceSeconds, cron.DefaultGraceSeconds)
	}
	// And the edit round trip keeps a 0.
	rec := fx.call(http.MethodPatch, "/api/crons/"+def.ID, fx.human(), `{"grace_seconds":0}`)
	if got, _ := db.GetCron(context.Background(), fx.pool, def.ID); rec.Code != http.StatusOK || got.GraceSeconds != 0 {
		t.Fatalf("patch 0: %d, grace %d", rec.Code, got.GraceSeconds)
	}
}

// MAJOR 7: a cron whose connection is gone can still be edited when its `mcp` is left as it is,
// whether the form sends it back unchanged or leaves it out; only a real change is validated.
func TestPatchUnchangedMCPSkipsUpstreamRevalidation(t *testing.T) {
	fx := newCronsFx(t)
	ctx := context.Background()
	gone := json.RawMessage(`[{"connection":"` + newUUID() + `","tools":{"echo":{"mode":"allow","hash":"abc"}}}]`)
	c := fx.newCron(func(x *db.Cron) { x.MCP = gone })

	// The same value, differently spaced and ordered (what a browser round trip does).
	reordered := `[{"tools":{"echo":{"hash":"abc","mode":"allow"}},"connection":` + string(mustJSON(t, firstConnection(t, gone))) + `}]`
	for name, body := range map[string]string{
		"mcp left out":          `{"prompt":"a new prompt"}`,
		"mcp sent back as is":   `{"prompt":"a new prompt","mcp":` + string(gone) + `}`,
		"mcp sent back shuffle": `{"prompt":"a new prompt","mcp":` + reordered + `}`,
	} {
		rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s, want 200", name, rec.Code, rec.Body.String())
		}
		got, _ := db.GetCron(ctx, fx.pool, c.ID)
		if got.Prompt != "a new prompt" || !jsonEqual(got.MCP, gone) {
			t.Fatalf("%s: prompt %q mcp %s", name, got.Prompt, got.MCP)
		}
	}
	// A real change is still judged against what exists.
	changed := `[{"connection":"` + newUUID() + `","tools":{"echo":{"mode":"allow","hash":"abc"}}}]`
	if rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"mcp":`+changed+`}`); rec.Code == http.StatusOK {
		t.Fatalf("a changed mcp naming a connection that does not exist was accepted: %s", rec.Body.String())
	}
	// Removing the orphan is a change too, and is accepted.
	if rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"mcp":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("clearing mcp: %d %s", rec.Code, rec.Body.String())
	}
}

func firstConnection(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var sel []struct {
		Connection string `json:"connection"`
	}
	if err := json.Unmarshal(raw, &sel); err != nil || len(sel) == 0 {
		t.Fatal(err)
	}
	return sel[0].Connection
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSameMCP(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{`[]`, `null`, true},
		{``, `[]`, true},
		{`[{"a":1,"b":2}]`, `[ {"b":2, "a":1} ]`, true},
		{`[{"a":1}]`, `[{"a":2}]`, false},
		{`[]`, `[{"a":1}]`, false},
	} {
		if got := sameMCP(json.RawMessage(tc.a), json.RawMessage(tc.b)); got != tc.want {
			t.Errorf("sameMCP(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}

// MINOR 10: enabling and disabling is a plain toggle: no token is minted or revoked, nothing is
// paused, and it is distinct from pause and resume.
func TestEnabledToggleMintsAndRevokesNothing(t *testing.T) {
	fx := newCronsFx(t)
	ctx := context.Background()
	c, row := fx.create(nil)
	mints := len(fx.minter.minted())

	rec := fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	var v struct {
		Enabled      bool    `json:"enabled"`
		Status       string  `json:"status"`
		PausedReason *string `json:"paused_reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.Enabled || v.Status != "disabled" || v.PausedReason != nil {
		t.Fatalf("disabled view = %+v (%v)", v, err)
	}
	got, _ := db.GetCron(ctx, fx.pool, c.ID)
	if got.Enabled || got.PausedReason != nil || got.TokenID != row.TokenID {
		t.Fatalf("row = %+v", got)
	}
	if rev := fx.core.revokedList(); len(rev) != 0 {
		t.Fatalf("disabling revoked %v", rev)
	}

	rec = fx.call(http.MethodPatch, "/api/crons/"+c.ID, fx.human(), `{"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	got, _ = db.GetCron(ctx, fx.pool, c.ID)
	if !got.Enabled || !got.NextRunAt.After(time.Now()) || got.TokenID != row.TokenID {
		t.Fatalf("row after enable = %+v", got)
	}
	if n := len(fx.minter.minted()); n != mints {
		t.Fatalf("the toggle minted %d token(s)", n-mints)
	}
	if rev := fx.core.revokedList(); len(rev) != 0 {
		t.Fatalf("enabling revoked %v", rev)
	}
	if !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Errorf("view = %s", rec.Body.String())
	}
}
