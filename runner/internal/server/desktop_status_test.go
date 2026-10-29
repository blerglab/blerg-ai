package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestRefusedLegacyHelloRecordsStatusWithoutTrustingForgedIdentity(t *testing.T) {
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "false")
	fx := newPrivFixture(t)
	fx.hub.UnregisterIfCurrent(fx.dc)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL,status='connected',last_seen_at=now()-interval '1 hour' WHERE id=$1`, fx.dc.ID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	desktopConnect(t, srv.URL, "forged", "", fx.dc.Name, false)
	row, err := db.GetDesktop(t.Context(), fx.pool, fx.dc.ID)
	if err != nil || row.Status != "connected" {
		t.Fatal("forged identity changed status")
	}
	desktopConnect(t, srv.URL, spawnTokenDaemonTok, "", fx.dc.Name, false)
	var status, reason string
	var seen time.Time
	if err := fx.pool.QueryRow(t.Context(), `SELECT status,status_reason,last_seen_at FROM daemons WHERE id=$1`, fx.dc.ID).Scan(&status, &reason, &seen); err != nil {
		t.Fatal(err)
	}
	if status != "refused" || reason == "" || time.Since(seen) > time.Minute {
		t.Fatal("refusal not recorded")
	}
}
func TestDesktopCloseRecordsDisconnectBeforeSessionGrace(t *testing.T) {
	fx := newPrivFixture(t)
	handleDaemonDisconnect(fx.hub, fx.dc, fx.pool)
	var status, reason string
	if err := fx.pool.QueryRow(t.Context(), `SELECT status,status_reason FROM daemons WHERE id=$1`, fx.dc.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "disconnected" || reason == "" {
		t.Fatal("close not recorded")
	}
}
