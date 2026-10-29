package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestDesktopGracePreservesNewerRefusal(t *testing.T) {
	automaticCompatibility(t)
	fx := desktopUpgradeFixture(t)
	gen := newUUID()
	rev, err := db.BindLegacyDesktop(t.Context(), fx.pool, fx.dc.ID, gen)
	if err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: fx.dc.ID, Name: fx.dc.Name, Mode: "local", LegacyDesktop: true, Compatibility: true, LegacyRevision: rev, DesktopGeneration: gen, send: make(chan []byte, 8)}
	fx.hub.Register(dc)
	fx.hub.UnregisterIfCurrent(dc)
	fx.hub.recordDaemonClosed(t.Context(), dc, fx.pool)
	fx.hub.recordLegacyRefusal(t.Context(), fx.pool, dc.Name)
	before, _ := db.GetDesktop(t.Context(), fx.pool, dc.ID)
	if before.Status != "refused" {
		t.Fatal("fixture did not record refusal")
	}
	var status string
	var lost bool
	if err := fx.pool.QueryRow(t.Context(), `SELECT status,daemon_lost_at IS NOT NULL FROM sessions WHERE id=$1`, fx.pubSession).Scan(&status, &lost); err != nil || status != "running" || lost {
		t.Fatal("close or refusal bypassed session recovery grace", err)
	}
	// Invoke the end of the old connection's grace period without a wall-clock wait.
	finishDaemonDisconnect(fx.hub, dc, fx.pool)
	after, _ := db.GetDesktop(t.Context(), fx.pool, dc.ID)
	if after.Status != "refused" || after.StatusReason != desktopEnrollmentGuidance {
		t.Fatalf("grace overwrote newer refusal: status=%s reason=%s", after.Status, after.StatusReason)
	}
	if err := fx.pool.QueryRow(t.Context(), `SELECT status,daemon_lost_at IS NOT NULL FROM sessions WHERE id=$1`, fx.pubSession).Scan(&status, &lost); err != nil || status != "error" || !lost {
		t.Fatal("preserving the refusal skipped session-loss cleanup", err)
	}
}

func TestDesktopVerifiedPersonalRefusalUpdatesStaleStatus(t *testing.T) {
	fx := newPrivFixture(t)
	fx.hub.UnregisterIfCurrent(fx.dc)
	b := *fx.dc.DesktopBinding
	// Simulate a restart with the stale connected row and an obsolete credential.
	b.Revision++
	token := "bdt_" + strings.Repeat("f", 64)
	desktopFakeCore(t, fx.api, map[string]desktopauth.Binding{token: b})
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	desktopConnect(t, srv.URL, token, b.DesktopID, fx.dc.Name, false)
	d, err := db.GetDesktop(t.Context(), fx.pool, b.DesktopID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "refused" || d.StatusReason != desktopEnrollmentGuidance {
		t.Fatal("verified personal credential was refused but the offline desktop still reports connected")
	}
}

func TestDesktopPersonalRefusalPreservesLiveAndNewerConnections(t *testing.T) {
	fx := newPrivFixture(t)
	binding := *fx.dc.DesktopBinding
	binding.Revision++
	token := "bdt_" + strings.Repeat("f", 64)
	desktopFakeCore(t, fx.api, map[string]desktopauth.Binding{token: binding})
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	desktopConnect(t, srv.URL, token, binding.DesktopID, fx.dc.Name, false)
	d, err := db.GetDesktop(t.Context(), fx.pool, fx.dc.ID)
	if err != nil || d.Status != "connected" || d.Generation != fx.dc.DesktopGeneration {
		t.Fatal("refusal overwrote live connection", err)
	}
	fx.hub.UnregisterIfCurrent(fx.dc)
	observed := *d
	newer := newUUID()
	if err := db.BindDesktopConnection(t.Context(), fx.pool, *fx.dc.DesktopBinding, fx.dc.Name, "local", "/repos", newer); err != nil {
		t.Fatal(err)
	}
	err = db.WithDesktopFence(t.Context(), fx.pool, fx.dc.ID, func(ctx context.Context) error { fx.hub.recordPersonalRefusal(ctx, fx.pool, observed); return nil })
	if err != nil {
		t.Fatal(err)
	}
	d, err = db.GetDesktop(t.Context(), fx.pool, fx.dc.ID)
	if err != nil || d.Status != "connected" || d.Generation != newer {
		t.Fatal("refusal overwrote newer persisted generation", err)
	}
	// An unverified token and a claimed ID cannot change even a stale row.
	desktopConnect(t, srv.URL, "bdt_"+strings.Repeat("0", 64), fx.dc.ID, fx.dc.Name, false)
	d, err = db.GetDesktop(t.Context(), fx.pool, fx.dc.ID)
	if err != nil || d.Status != "connected" {
		t.Fatal("unauthenticated refusal changed status", err)
	}
}
