package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type stopAuditSQL struct{}
type stopAuditBarrier struct {
	armed                      atomic.Bool
	reached, release, inserted chan struct{}
}

func (b *stopAuditBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, stopAuditSQL{}, d.SQL)
}
func (b *stopAuditBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	q, _ := ctx.Value(stopAuditSQL{}).(string)
	if strings.Contains(q, "SELECT repo,project_path FROM session_runtime") && b.armed.CompareAndSwap(true, false) {
		close(b.reached)
		<-b.release
	}
	if strings.Contains(q, "INSERT INTO sessions (id, daemon_id, status") {
		select {
		case <-b.inserted:
		default:
			close(b.inserted)
		}
	}
}
func TestRuntimeAdmittedStartCannotReviveStoppedSession(t *testing.T) {
	_, hub, host, pool := spawnTokenFixture(t)
	ctx := context.Background()
	const sid = "00000000-0000-4000-8000-000000000919"
	if err := db.InsertClusterSession(ctx, pool, sid, host.ID, "starting", "/repos/fixture", "fixture", "", ""); err != nil {
		t.Fatal(err)
	}
	raw, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b := &stopAuditBarrier{reached: make(chan struct{}), release: make(chan struct{}), inserted: make(chan struct{})}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = b
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	srv := httptest.NewServer(hub.ServeDaemon(spawnTokenDaemonTok, traced))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Token: raw, ActiveSessions: []string{sid}}); err != nil {
		t.Fatal(err)
	}
	b.armed.Store(true)
	if err = conn.WriteJSON(protocol.SessionStarted{Type: "session_started", SessionID: sid, Repo: "fixture", ProjectPath: "/repos/fixture"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.reached:
	case <-time.After(3 * time.Second):
		t.Fatal("start scope check not reached")
	}
	// Mirrors the browser/orphan Stop path's durable terminal write and revocation.
	if _, err = pool.Exec(ctx, `UPDATE sessions SET status='stopped',ended_at=now() WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if err = db.RevokeBoardTokensForSession(ctx, pool, sid); err != nil {
		t.Fatal(err)
	}
	close(b.release)
	select {
	case <-b.inserted:
	case <-time.After(3 * time.Second):
		t.Fatal("start write did not complete")
	}
	s, err := db.GetSession(ctx, pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != "stopped" || s.EndedAt == nil {
		t.Fatal("admitted runtime session_started revived stopped/revoked session")
	}
}
