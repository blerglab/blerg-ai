package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// What the server does around a desktop daemon restart (docs/design/agent-session-recovery.md).

// The package's tests run a daemon disconnect synchronously, as it ran before the grace period
// existed: almost all of them are about what the disconnect does, not when. The tests that are
// about the grace period set it themselves (withGrace).
func init() { daemonLostGrace = 0 }

func withGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := daemonLostGrace
	daemonLostGrace = d
	t.Cleanup(func() { daemonLostGrace = old })
}

func rowStatus(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	row, err := db.GetSession(ctx, pool, id)
	if err != nil || row == nil {
		t.Fatalf("session %s: %v", id, err)
	}
	return row.Status
}

// A desktop daemon that is back within the grace period was never away as far as anyone
// watching can tell: its sessions are not marked, and a message typed meanwhile reaches it.
func TestDaemonBackWithinGraceLeavesSessionsAlone(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	withGrace(t, 300*time.Millisecond)
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	hub.SetSessionOwner(sessionID, dc.ID)

	handleDaemonDisconnect(hub, dc, pool)
	if got := rowStatus(ctx, t, pool, sessionID); got != "running" {
		t.Fatalf("status right after the disconnect = %q: it must not be marked inside the grace period", got)
	}
	// A message from the browser waits for the daemon; one through the contract is told to retry.
	raw := []byte(`{"type":"agent_user_message","session_id":"` + sessionID + `","text":"typed during the restart"}`)
	if !hub.HoldForAwayDaemon(sessionID, raw) {
		t.Fatal("a message for a session of a daemon in its grace period was not held")
	}
	if _, apiErr := api.SendMessage(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, sessionID, "hello", ""); apiErr == nil || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("contract message during the grace period: %+v, want 503", apiErr)
	}

	// The daemon is back: the hello reconciles, registers, and hands over what was held.
	dc2 := &DaemonConn{ID: dc.ID, Name: dc.Name, ReposRoot: dc.ReposRoot, send: make(chan []byte, 8)}
	reconcileSessions(ctx, hub, pool, dc.ID, []string{sessionID}, nil)
	hub.Register(dc2)
	hub.deliverHeld(dc2)
	select {
	case got := <-dc2.send:
		if string(got) != string(raw) {
			t.Errorf("delivered %s", got)
		}
	default:
		t.Fatal("the held message was not handed to the returning daemon")
	}
	time.Sleep(500 * time.Millisecond) // past the grace period
	if got := rowStatus(ctx, t, pool, sessionID); got != "running" {
		t.Fatalf("status after the grace period = %q: the daemon was back in time", got)
	}
	if hub.HoldForAwayDaemon(sessionID, raw) {
		t.Error("messages are still being held for a daemon that is connected")
	}
}

// A daemon that does not come back is treated exactly as before, a grace period later.
func TestDaemonGoneAfterGraceMarksSessionsLost(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	withGrace(t, 150*time.Millisecond)
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "idle", nil); err != nil {
		t.Fatal(err)
	}
	hub.SetSessionOwner(sessionID, dc.ID)
	handleDaemonDisconnect(hub, dc, pool)
	if !hub.HoldForAwayDaemon(sessionID, []byte(`{}`)) {
		t.Fatal("not held")
	}
	// Wait it out without touching the database: the fixture's schema is selected on the pool's
	// one connection (SET search_path), so a query here while the grace timer runs its own would
	// open a second connection that cannot see the tables.
	time.Sleep(1500 * time.Millisecond)
	if got := rowStatus(ctx, t, pool, sessionID); got != "error" {
		t.Fatalf("the session of a daemon that never came back is %q, want error", got)
	}
	if awaits, _ := db.SessionAwaitsDaemon(ctx, pool, sessionID); !awaits {
		t.Error("the lost session is not marked as waiting for its daemon")
	}
	if hub.HoldForAwayDaemon(sessionID, []byte(`{}`)) {
		t.Error("messages are held for a daemon that is gone")
	}
	// It is still revivable, as before.
	reconcileSessions(ctx, hub, pool, dc.ID, []string{sessionID}, map[string]string{sessionID: "idle"})
	if got := rowStatus(ctx, t, pool, sessionID); got != "idle" {
		t.Errorf("revived status = %q", got)
	}
}

// A cluster pod's disconnect is not held: "disconnected" is not an ending, and a message
// resumes the session.
func TestClusterPodDisconnectIsNotHeld(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	withGrace(t, time.Hour)
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	dc.Mode = "runner"
	handleDaemonDisconnect(hub, dc, pool)
	if got := rowStatus(ctx, t, pool, sessionID); got != "disconnected" {
		t.Fatalf("status = %q, want disconnected at once", got)
	}
}

func killsSent(dc *DaemonConn) []string {
	var out []string
	for {
		select {
		case raw := <-dc.send:
			var k protocol.KillSession
			if json.Unmarshal(raw, &k) == nil && k.Type == "kill_session" {
				out = append(out, k.SessionID)
			}
		default:
			return out
		}
	}
}

// A session stopped while its daemon was away stays stopped when the daemon returns still
// hosting it (it brought it back from a recovery record): the daemon is told to end it.
func TestReconcileDoesNotReviveAStoppedSession(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	for _, reason := range []string{db.EndReasonStoppedByUser, db.EndReasonStoppedByAgent, db.EndReasonAutoStopped} {
		sessionID, _ := spawnViaREST(t, api, dc, "app")
		now := time.Now()
		if err := db.EndSessionStatus(ctx, pool, sessionID, "ended", &now, systemEnd(reason)); err != nil {
			t.Fatal(err)
		}
		killsSent(dc) // drain what the spawn sent

		stopped := reconcileSessions(ctx, hub, pool, dc.ID, []string{sessionID}, map[string]string{sessionID: "idle"})
		if len(stopped) != 1 || stopped[0] != sessionID {
			t.Fatalf("%s: reconcile returned %v, want the stopped session", reason, stopped)
		}
		row, _ := db.GetSession(ctx, pool, sessionID)
		if row == nil || row.Status != "ended" || row.EndReason == nil || *row.EndReason != reason {
			t.Fatalf("%s: a stopped session was revived: %+v", reason, row)
		}
		killStoppedSessions(dc, stopped)
		if got := killsSent(dc); len(got) != 1 || got[0] != sessionID {
			t.Errorf("%s: kill_session sent for %v", reason, got)
		}
	}
}

// Cleared from the app after the lost-daemon sweep had already ended it: the row is "stopped"
// and keeps the sweep's reason. That is a stop too.
func TestReconcileDoesNotReviveASessionClearedAfterItWasLost(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	now := time.Now()
	if err := db.EndSessionStatus(ctx, pool, sessionID, "error", &now, systemEnd(db.EndReasonDaemonLost)); err != nil {
		t.Fatal(err)
	}
	// What the delete of an orphaned session writes: its own reason does not replace the first.
	setSessionStatusEnd(ctx, hub, pool, sessionID, "stopped", &now, nil, true, systemEnd(db.EndReasonStoppedByUser))
	row, _ := db.GetSession(ctx, pool, sessionID)
	if row == nil || row.Status != "stopped" || row.EndReason == nil || *row.EndReason != db.EndReasonDaemonLost {
		t.Fatalf("precondition: %+v", row)
	}
	if stopped := reconcileSessions(ctx, hub, pool, dc.ID, []string{sessionID}, nil); len(stopped) != 1 {
		t.Fatalf("a session cleared from the app was revived (kills: %v)", stopped)
	}
	if got := rowStatus(ctx, t, pool, sessionID); got != "stopped" {
		t.Errorf("status = %q", got)
	}
}

// Losing sight of a session is not a decision to stop it: a daemon that returns after the
// lost-daemon window, still hosting the session, gets it back, as before.
func TestReconcileStillRevivesALostSession(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	for _, reason := range []string{db.EndReasonDaemonLost, db.EndReasonDaemonUnreported} {
		sessionID, _ := spawnViaREST(t, api, dc, "app")
		now := time.Now()
		if err := db.EndSessionStatus(ctx, pool, sessionID, "error", &now, systemEnd(reason)); err != nil {
			t.Fatal(err)
		}
		if stopped := reconcileSessions(ctx, hub, pool, dc.ID, []string{sessionID}, map[string]string{sessionID: "idle"}); len(stopped) != 0 {
			t.Fatalf("%s: treated as a stop: %v", reason, stopped)
		}
		if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "idle" || row.EndReason != nil {
			t.Fatalf("%s: not revived: %+v", reason, row)
		}
	}
	// And the plain restart: marked lost by the disconnect, back within the window.
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	handleDaemonDisconnect(hub, dc, pool)
	if stopped := reconcileSessions(ctx, hub, pool, dc.ID, []string{sessionID}, nil); len(stopped) != 0 {
		t.Fatalf("a restart was treated as a stop: %v", stopped)
	}
	if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "running" {
		t.Fatalf("after the restart: %+v", row)
	}
}

// A disconnect cleanup that the daemon's reconnect has overtaken marks nothing: the new
// connection's hello has already said which sessions are alive.
func TestOvertakenDisconnectMarksNothing(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "idle", nil); err != nil {
		t.Fatal(err)
	}
	// The cleanup has claimed the old connection, and the daemon is back before the cleanup
	// reaches the sessions.
	if !hub.UnregisterIfCurrent(dc) {
		t.Fatal("the fixture's connection was not the current one")
	}
	hub.Register(&DaemonConn{ID: dc.ID, Name: dc.Name, ReposRoot: dc.ReposRoot, send: make(chan []byte, 8)})
	if marked := markDaemonSessionsLost(ctx, hub, dc, pool); len(marked) != 0 {
		t.Errorf("marked %v", marked)
	}
	if row, _ := db.GetSession(ctx, pool, sessionID); row == nil || row.Status != "idle" {
		t.Fatalf("a session of a daemon that had already reconnected was marked lost: %+v", row)
	}
}

// A message that has nowhere to go is refused, not answered "ok" and dropped.
func TestSendMessageWithNoDaemonIsRefused(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	key := runnerPrincipal{Kind: runnerKeyPrincipalKind}

	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "idle", nil); err != nil {
		t.Fatal(err)
	}
	hub.SetSessionOwner(sessionID, dc.ID) // as session_started (or a hello's reconcile) does
	if _, apiErr := api.SendMessage(ctx, key, sessionID, "hello", ""); apiErr != nil {
		t.Fatalf("with its daemon connected: %v", apiErr)
	}

	// The daemon restarts: the session waits for its host. Retryable.
	handleDaemonDisconnect(hub, dc, pool)
	_, apiErr := api.SendMessage(ctx, key, sessionID, "hello", "")
	if apiErr == nil || apiErr.Status != http.StatusServiceUnavailable || apiErr.RetryAfter <= 0 {
		t.Fatalf("while the daemon is away: %+v, want 503 with Retry-After", apiErr)
	}
	rec := httptest.NewRecorder()
	writeAPIError(rec, apiErr)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("response: %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}

	// It never came back and was finalised: ended.
	cutoff := time.Now().Add(time.Minute)
	if done, err := db.FinishLostDaemonSession(ctx, pool, sessionID, reasonDaemonLost, cutoff); err != nil || !done {
		t.Fatalf("finalise: %v %v", done, err)
	}
	if _, apiErr := api.SendMessage(ctx, key, sessionID, "hello", ""); apiErr == nil || apiErr.Status != http.StatusConflict {
		t.Fatalf("to an ended session: %+v, want 409", apiErr)
	}
}

func TestUndeliverable(t *testing.T) {
	for _, c := range []struct {
		status string
		awaits bool
		want   int
	}{
		{"disconnected", false, 0}, // a cluster session: the message resumes it
		{"running", false, http.StatusServiceUnavailable},
		{"idle", false, http.StatusServiceUnavailable},
		{"starting", false, http.StatusServiceUnavailable},
		{"error", true, http.StatusServiceUnavailable}, // marked lost by a disconnect
		{"error", false, http.StatusConflict},          // failed for a reason of its own: retrying never helps
		{"stopped", false, http.StatusConflict},
		{"stopped", true, http.StatusConflict},
		{"ended", false, http.StatusConflict},
	} {
		got := 0
		if e := undeliverable(&db.SessionRow{Status: c.status}, c.awaits); e != nil {
			got = e.Status
		}
		if got != c.want {
			t.Errorf("undeliverable(%s, awaits=%v) = %d, want %d", c.status, c.awaits, got, c.want)
		}
	}
}
