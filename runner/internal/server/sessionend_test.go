package server

// Session end attribution (migration 018): every path that ends a session says
// why, once, and the first recorded reason wins.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// endFixture is a desktop session on a connected fake daemon, with browser
// auth wired so DELETE /api/sessions/{id} can be called as "user-1".
type endFixture struct {
	api       *API
	hub       *Hub
	pool      *pgxpool.Pool
	dc        *DaemonConn
	sessionID string
	bearer    string
	mux       *http.ServeMux
	// mintAs mints a browser token for any account, for viewer tests.
	mintAs func(sub string) string
}

const endTestDaemonID = "00000000-0000-4000-8000-0000000000e1"

func newEndFixture(t *testing.T, status string, sendBuf int) *endFixture {
	t.Helper()
	api, hub, pool := clusterRunnerAPI(t, &fakeK8s{})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	mintAs := func(sub string) string {
		return mintRunnerToken(t, priv, "core-1", identity.Claims{
			Sub: sub, Aud: coreAuthAudience, Kind: "human",
			Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
		})
	}
	ctx := context.Background()
	if err := db.UpsertDaemon(ctx, pool, endTestDaemonID, "desktop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, endTestDaemonID, status,
		"/repos/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: endTestDaemonID, Name: "desktop", send: make(chan []byte, sendBuf)}
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/sessions/{id}", api.HandleDeleteSession)
	mux.HandleFunc("GET /api/sessions", api.HandleGetSessions)
	return &endFixture{
		api: api, hub: hub, pool: pool, dc: dc, sessionID: sessionID,
		bearer: mintAs("user-1"), mux: mux, mintAs: mintAs,
	}
}

// own makes the fake daemon the session's owner (a live session).
func (f *endFixture) own() {
	f.hub.Register(f.dc)
	f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
}

func (f *endFixture) delete(t *testing.T) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+f.sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+f.bearer)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code
}

func (f *endFixture) row(t *testing.T) *db.SessionRow {
	t.Helper()
	row, err := db.GetSession(context.Background(), f.pool, f.sessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v (row %v)", err, row)
	}
	return row
}

// wantEnd checks the stored attribution.
func wantEnd(t *testing.T, row *db.SessionRow, reason, kind, account string) {
	t.Helper()
	if got := derefOrEmpty(row.EndReason); got != reason {
		t.Errorf("end_reason = %q, want %q", got, reason)
	}
	if got := derefOrEmpty(row.EndedByKind); got != kind {
		t.Errorf("ended_by_kind = %q, want %q", got, kind)
	}
	if got := derefOrEmpty(row.EndedByAccount); got != account {
		t.Errorf("ended_by_account = %q, want %q", got, account)
	}
}

// Every ending path, and what it records. Each case starts from a session in
// `from` and runs one path.
func TestSessionEndAttributionPerPath(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		from        string
		run         func(t *testing.T, f *endFixture)
		wantStatus  string
		wantReason  string
		wantKind    string
		wantAccount string
	}{
		{
			// (a) the browser stop: recorded with the request, kept through
			// the process exit the kill causes.
			name: "browser DELETE of a live session, then the exit it causes",
			from: "running",
			run: func(t *testing.T, f *endFixture) {
				f.own()
				if code := f.delete(t); code != http.StatusNoContent {
					t.Fatalf("DELETE = %d, want 204", code)
				}
				select {
				case raw := <-f.dc.send:
					var m protocol.KillSession
					if err := json.Unmarshal(raw, &m); err != nil || m.Type != "kill_session" {
						t.Fatalf("daemon got %s, want kill_session", raw)
					}
				default:
					t.Fatal("no kill_session sent")
				}
				// Recorded, but not reported: the session is still running.
				if r, _ := endAttribution(f.row(t), "user-1"); r != "" {
					t.Errorf("a live session reports end_reason %q", r)
				}
				HandleSessionEnded(ctx, f.hub, f.pool, protocol.SessionEnded{
					Type: "session_ended", SessionID: f.sessionID, ExitCode: 0,
				})
			},
			wantStatus: "stopped", wantReason: db.EndReasonStoppedByUser,
			wantKind: db.EndedByHuman, wantAccount: "user-1",
		},
		{
			// (b) an agent token through the runner contract.
			name: "runner stop by an agent token",
			from: "running",
			run: func(t *testing.T, f *endFixture) {
				p := runnerPrincipal{Kind: "agent", Sub: "tok-a", OnBehalfOf: "acct-a"}
				if err := db.SetSessionSpawningAccount(ctx, f.pool, f.sessionID, "acct-a"); err != nil {
					t.Fatal(err)
				}
				if _, apiErr := f.api.Stop(ctx, p, f.sessionID); apiErr != nil {
					t.Fatalf("Stop: %v", apiErr)
				}
			},
			wantStatus: "ended", wantReason: db.EndReasonStoppedByAgent,
			wantKind: db.EndedByAgent, wantAccount: "acct-a",
		},
		{
			name: "runner stop by the static runner key",
			from: "running",
			run: func(t *testing.T, f *endFixture) {
				if _, apiErr := f.api.Stop(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, f.sessionID); apiErr != nil {
					t.Fatalf("Stop: %v", apiErr)
				}
			},
			wantStatus: "ended", wantReason: db.EndReasonStoppedByAgent,
			wantKind: db.EndedByRunnerKey,
		},
		{
			// (d) the process exits with nobody having asked.
			name: "process exits cleanly on its own",
			from: "running",
			run: func(t *testing.T, f *endFixture) {
				HandleSessionEnded(ctx, f.hub, f.pool, protocol.SessionEnded{
					Type: "session_ended", SessionID: f.sessionID, ExitCode: 0,
				})
			},
			wantStatus: "stopped", wantReason: db.EndReasonProcessExited,
		},
		{
			name: "process exits non-zero on its own",
			from: "running",
			run: func(t *testing.T, f *endFixture) {
				HandleSessionEnded(ctx, f.hub, f.pool, protocol.SessionEnded{
					Type: "session_ended", SessionID: f.sessionID, ExitCode: 2,
				})
			},
			wantStatus: "error", wantReason: db.EndReasonProcessFailed,
		},
		{
			name: "daemon reports stopped",
			from: "running",
			run: func(t *testing.T, f *endFixture) {
				if err := HandleSessionStateChanged(ctx, f.hub, f.pool, protocol.SessionStateChanged{
					Type: "session_state_changed", SessionID: f.sessionID, Status: "stopped",
					// A daemon cannot supply its own attribution.
					EndReason: db.EndReasonStoppedByUser, EndedBy: &protocol.EndedBy{Kind: "human", Self: true},
				}); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: "stopped", wantReason: db.EndReasonProcessExited,
		},
		{
			// (e) a revivable error is not an ending: nothing recorded.
			name: "daemon reports a revivable error",
			from: "running",
			run: func(t *testing.T, f *endFixture) {
				msg := "engine crashed"
				if err := HandleSessionStateChanged(ctx, f.hub, f.pool, protocol.SessionStateChanged{
					Type: "session_state_changed", SessionID: f.sessionID, Status: "error", Message: &msg,
				}); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: "error", wantReason: "",
		},
		{
			// (e) a start that failed: the reason is start_failed, the detail
			// stays in error_reason.
			name: "cluster start failed",
			from: "starting",
			run: func(t *testing.T, f *endFixture) {
				f.api.markClusterStartFailed(ctx, f.sessionID, "no job")
				if got := derefOrEmpty(f.row(t).ErrorReason); got != "no job" {
					t.Errorf("error_reason = %q, want the start failure text", got)
				}
			},
			wantStatus: "error", wantReason: db.EndReasonStartFailed,
		},
		{
			// (f) the daemon is connected and no longer reports the session.
			name: "reconcile: daemon no longer reports it",
			from: "running",
			run: func(t *testing.T, f *endFixture) {
				reconcileSessions(ctx, f.hub, f.pool, endTestDaemonID, nil, nil)
			},
			wantStatus: "stopped", wantReason: db.EndReasonDaemonUnreported,
		},
		{
			// (g) DELETE of a row no daemon owns any more.
			name: "browser DELETE of an orphaned session",
			from: "error",
			run: func(t *testing.T, f *endFixture) {
				if code := f.delete(t); code != http.StatusNoContent {
					t.Fatalf("DELETE = %d, want 204", code)
				}
			},
			wantStatus: "stopped", wantReason: db.EndReasonStoppedByUser,
			wantKind: db.EndedByHuman, wantAccount: "user-1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEndFixture(t, tc.from, 8)
			tc.run(t, f)
			row := f.row(t)
			if row.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", row.Status, tc.wantStatus)
			}
			wantEnd(t, row, tc.wantReason, tc.wantKind, tc.wantAccount)
		})
	}
}

// (c) auto_stop records its own reason in the same statement as its claim.
func TestAutoStopRecordsItsReason(t *testing.T) {
	api, pool, sessionID := autoStopSession(t, true)
	turnDone(t, api, pool, sessionID)
	row, err := db.GetSession(context.Background(), pool, sessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.Status != "ended" {
		t.Fatalf("status = %q, want ended", row.Status)
	}
	wantEnd(t, row, db.EndReasonAutoStopped, "", "")
}

// (f) the reconciler's Job verdicts each map to their own code.
func TestReconcilerRecordsJobEndReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		job    map[string]any
		reason string
	}{
		{"succeeded", map[string]any{"succeeded": 1}, db.EndReasonJobFinished},
		{"failed", map[string]any{"failed": 1}, db.EndReasonJobFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReconcileFixture(t, "running")
			f.fake.setJobStatus(f.jobName, tc.job)
			f.api.reconcileClusterJobsOnce(context.Background())
			row, _ := db.GetSession(context.Background(), f.pool, f.sessionID)
			wantEnd(t, row, tc.reason, "", "")
		})
	}
}

// (f) a desktop session whose daemon never came back.
func TestDaemonLostSweepRecordsReason(t *testing.T) {
	f := newReconcileFixture(t, "running")
	f.api.daemonLostWindow = time.Hour
	ctx := context.Background()
	if err := db.SetSessionPosture(ctx, f.pool, f.sessionID, "daemon", false); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionStatus(ctx, f.pool, f.sessionID, "error", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE sessions SET daemon_lost_at = now() - interval '2 hours' WHERE id = $1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	f.api.reconcileClusterJobsOnce(ctx)
	row, _ := db.GetSession(ctx, f.pool, f.sessionID)
	wantEnd(t, row, db.EndReasonDaemonLost, "", "")
}

// First write wins: a session a person stopped keeps that attribution when a
// broker's stop, the orphan cleanup, or the process exit comes after it.
func TestSessionEndFirstWriteWins(t *testing.T) {
	ctx := context.Background()
	f := newEndFixture(t, "running", 8)
	f.own()
	if code := f.delete(t); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}
	HandleSessionEnded(ctx, f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sessionID, ExitCode: 1})
	if _, apiErr := f.api.Stop(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, f.sessionID); apiErr != nil {
		t.Fatal(apiErr)
	}
	wantEnd(t, f.row(t), db.EndReasonStoppedByUser, db.EndedByHuman, "user-1")

	// An orphan cleanup of a session that already ended for its own reason
	// does not re-attribute it to whoever cleared the row.
	g := newEndFixture(t, "running", 8)
	HandleSessionEnded(ctx, g.hub, g.pool, protocol.SessionEnded{Type: "session_ended", SessionID: g.sessionID, ExitCode: 3})
	if code := g.delete(t); code != http.StatusNoContent {
		t.Fatalf("orphan DELETE = %d", code)
	}
	wantEnd(t, g.row(t), db.EndReasonProcessFailed, "", "")
}

// A kill that never left takes back the attribution the request wrote.
func TestDeleteWithFullSendBufferRecordsNothing(t *testing.T) {
	f := newEndFixture(t, "running", 0)
	f.own()
	if code := f.delete(t); code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE = %d, want 503", code)
	}
	wantEnd(t, f.row(t), "", "", "")
}

// A session the daemon reports alive again has not ended: reviving it clears
// what was recorded.
func TestRevivedSessionForgetsItsEnd(t *testing.T) {
	ctx := context.Background()
	f := newEndFixture(t, "running", 8)
	reconcileSessions(ctx, f.hub, f.pool, endTestDaemonID, nil, nil)
	wantEnd(t, f.row(t), db.EndReasonDaemonUnreported, "", "")
	reconcileSessions(ctx, f.hub, f.pool, endTestDaemonID, []string{f.sessionID}, map[string]string{f.sessionID: "idle"})
	row := f.row(t)
	if row.Status != "idle" {
		t.Fatalf("status = %q, want idle (revived)", row.Status)
	}
	wantEnd(t, row, "", "", "")
}

// listAs fetches GET /api/sessions as the given account and returns this
// fixture's session as raw JSON.
func (f *endFixture) listAs(t *testing.T, sub string) (map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+f.mintAs(sub))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions as %s = %d", sub, rec.Code)
	}
	body := rec.Body.String()
	var list []map[string]any
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s["id"] == f.sessionID {
			return s, body
		}
	}
	t.Fatalf("session %s not listed", f.sessionID)
	return nil, ""
}

// The same session, ended by account user-1, reads "you" to user-1 and not to
// anyone else — and the account id itself reaches neither: not in the list,
// not in any broadcast.
func TestEndedByIsPerViewerAndNeverCarriesTheAccount(t *testing.T) {
	f := newEndFixture(t, "running", 8)
	// Two browsers, one per account, to catch the live broadcast.
	mine := make(chan []byte, 16)
	theirs := make(chan []byte, 16)
	f.hub.RegisterBrowser(&BrowserConn{ID: "b-mine", AccountID: "user-1", send: mine})
	f.hub.RegisterBrowser(&BrowserConn{ID: "b-theirs", AccountID: "user-2", send: theirs})
	f.own()
	if code := f.delete(t); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}
	HandleSessionEnded(context.Background(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sessionID})

	for _, tc := range []struct {
		viewer   string
		wantSelf bool
	}{{"user-1", true}, {"user-2", false}} {
		info, body := f.listAs(t, tc.viewer)
		by, _ := info["ended_by"].(map[string]any)
		if info["end_reason"] != db.EndReasonStoppedByUser || by["kind"] != "human" {
			t.Errorf("%s: end fields = %v / %v", tc.viewer, info["end_reason"], info["ended_by"])
		}
		if self, _ := by["self"].(bool); self != tc.wantSelf {
			t.Errorf("%s: ended_by.self = %v, want %v", tc.viewer, self, tc.wantSelf)
		}
		if strings.Contains(body, "account_id") || strings.Contains(body, `"user-1"`) {
			t.Errorf("%s: list response carries an account id: %s", tc.viewer, body)
		}
	}

	// The session_ended broadcast each browser got.
	ended := func(ch chan []byte) (map[string]any, string) {
		for {
			select {
			case raw := <-ch:
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				if m["type"] == "session_ended" {
					return m, string(raw)
				}
			default:
				t.Fatal("no session_ended broadcast")
				return nil, ""
			}
		}
	}
	for _, tc := range []struct {
		name     string
		ch       chan []byte
		wantSelf bool
	}{{"mine", mine, true}, {"theirs", theirs, false}} {
		m, raw := ended(tc.ch)
		by, _ := m["ended_by"].(map[string]any)
		if self, _ := by["self"].(bool); self != tc.wantSelf || by["kind"] != "human" {
			t.Errorf("%s broadcast ended_by = %v, want kind human self %v", tc.name, m["ended_by"], tc.wantSelf)
		}
		if strings.Contains(raw, "account_id") || strings.Contains(raw, "user-1") {
			t.Errorf("%s broadcast carries an account id: %s", tc.name, raw)
		}
	}
}

// An agent token's stop reads "yours" to the account it acts for.
func TestEndedBySelfForYourOwnAgentToken(t *testing.T) {
	f := newEndFixture(t, "running", 8)
	if err := db.SetSessionSpawningAccount(context.Background(), f.pool, f.sessionID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, apiErr := f.api.Stop(context.Background(),
		runnerPrincipal{Kind: "agent", Sub: "tok-a", OnBehalfOf: "user-1"}, f.sessionID); apiErr != nil {
		t.Fatal(apiErr)
	}
	for viewer, want := range map[string]bool{"user-1": true, "user-2": false} {
		info, _ := f.listAs(t, viewer)
		by, _ := info["ended_by"].(map[string]any)
		if self, _ := by["self"].(bool); by["kind"] != "agent" || self != want {
			t.Errorf("%s: ended_by = %v, want agent self %v", viewer, info["ended_by"], want)
		}
	}
}

// The runner contract and the result/webhook body carry the kind only — no
// "self" (there is no viewer) and never an account.
func TestEndAttributionSerialization(t *testing.T) {
	ctx := context.Background()
	f := newEndFixture(t, "error", 8)
	if code := f.delete(t); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}

	p := runnerPrincipal{Kind: runnerKeyPrincipalKind}
	status, apiErr := f.api.SessionStatus(ctx, p, f.sessionID)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	sraw, _ := json.Marshal(status)
	var s map[string]any
	_ = json.Unmarshal(sraw, &s)
	sby, _ := s["ended_by"].(map[string]any)
	if s["end_reason"] != db.EndReasonStoppedByUser || sby["kind"] != "human" {
		t.Errorf("status end fields = %v / %v", s["end_reason"], s["ended_by"])
	}
	if len(sby) != 1 {
		t.Errorf("runner status ended_by = %v, want the kind alone", sby)
	}

	res, apiErr := f.api.Result(ctx, p, f.sessionID)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	rraw, _ := json.Marshal(res)
	var r map[string]any
	_ = json.Unmarshal(rraw, &r)
	rby, _ := r["ended_by"].(map[string]any)
	if r["end_reason"] != db.EndReasonStoppedByUser || rby["kind"] != "human" {
		t.Errorf("result end fields = %v / %v", r["end_reason"], r["ended_by"])
	}
	if len(rby) != 1 {
		t.Errorf("result ended_by = %v, want the kind alone", rby)
	}

	// A legacy/live row: every field present, empty/null, nothing invented.
	g := newEndFixture(t, "running", 8)
	gres, apiErr := g.api.Result(ctx, p, g.sessionID)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	graw, _ := json.Marshal(gres)
	var gr map[string]any
	_ = json.Unmarshal(graw, &gr)
	if v, ok := gr["end_reason"]; !ok || v != "" {
		t.Errorf("live result end_reason = %v (present %v), want \"\"", v, ok)
	}
	if v, ok := gr["ended_by"]; !ok || v != nil {
		t.Errorf("live result ended_by = %v (present %v), want null", v, ok)
	}
}

// staleStop records a browser stop on a live session and ages it past the
// pending-stop grace, as if the kill had been lost.
func staleStop(t *testing.T, f *endFixture) {
	t.Helper()
	f.own()
	if code := f.delete(t); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}
	<-f.dc.send // the kill_session, which this daemon "loses"
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE sessions SET end_recorded_at = now() - interval '5 minutes' WHERE id = $1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
}

// A stop whose session kept going is not blamed for how it ends later: once
// the session is reported alive past the grace, the real ending's own reason
// wins — whichever path it comes by, and whether "alive" was a transition or
// just a heartbeat.
func TestStaleStopLosesToTheRealEnding(t *testing.T) {
	ctx := context.Background()
	alive := map[string]func(t *testing.T, f *endFixture){
		"status transition": func(t *testing.T, f *endFixture) {
			if err := HandleSessionStateChanged(ctx, f.hub, f.pool, protocol.SessionStateChanged{
				Type: "session_state_changed", SessionID: f.sessionID, Status: "waiting",
			}); err != nil {
				t.Fatal(err)
			}
		},
		"heartbeat, no transition": func(t *testing.T, f *endFixture) {
			reconcileSessions(ctx, f.hub, f.pool, endTestDaemonID, []string{f.sessionID}, map[string]string{f.sessionID: "running"})
		},
	}
	endings := []struct {
		name   string
		end    func(t *testing.T, f *endFixture)
		reason string
	}{
		{"process exits", func(t *testing.T, f *endFixture) {
			HandleSessionEnded(ctx, f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sessionID})
		}, db.EndReasonProcessExited},
		{"process fails", func(t *testing.T, f *endFixture) {
			HandleSessionEnded(ctx, f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sessionID, ExitCode: 1})
		}, db.EndReasonProcessFailed},
		{"auto_stop", func(t *testing.T, f *endFixture) {
			if err := db.SetSessionAutoStop(ctx, f.pool, f.sessionID, true); err != nil {
				t.Fatal(err)
			}
			f.api.autoStopOnTurnDone(f.sessionID)
		}, db.EndReasonAutoStopped},
	}
	for aliveName, reportAlive := range alive {
		for _, e := range endings {
			t.Run(aliveName+"/"+e.name, func(t *testing.T) {
				f := newEndFixture(t, "running", 8)
				staleStop(t, f)
				reportAlive(t, f)
				e.end(t, f)
				wantEnd(t, f.row(t), e.reason, "", "")
			})
		}
	}
}

// The other side of the grace: a dying session that reports one last live
// status while the kill is landing (an agent whose cancelled turn goes idle)
// keeps the stop.
func TestStopSurvivesALastStatusWhileTheKillLands(t *testing.T) {
	ctx := context.Background()
	f := newEndFixture(t, "running", 8)
	f.own()
	if code := f.delete(t); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}
	if err := HandleSessionStateChanged(ctx, f.hub, f.pool, protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: f.sessionID, Status: "idle",
	}); err != nil {
		t.Fatal(err)
	}
	reconcileSessions(ctx, f.hub, f.pool, endTestDaemonID, []string{f.sessionID}, map[string]string{f.sessionID: "idle"})
	HandleSessionEnded(ctx, f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sessionID})
	wantEnd(t, f.row(t), db.EndReasonStoppedByUser, db.EndedByHuman, "user-1")
}

// Two overlapping DELETEs: the first stop's kill goes out, the second finds
// the daemon's buffer full and answers 503. The second never wrote the
// attribution, so it must not take the first one's back.
func TestOverlappingDeleteKeepsTheFirstStop(t *testing.T) {
	f := newEndFixture(t, "running", 1)
	f.own()
	if code := f.delete(t); code != http.StatusNoContent {
		t.Fatalf("first DELETE = %d, want 204", code)
	}
	if code := f.delete(t); code != http.StatusServiceUnavailable {
		t.Fatalf("second DELETE = %d, want 503", code)
	}
	wantEnd(t, f.row(t), db.EndReasonStoppedByUser, db.EndedByHuman, "user-1")
}
