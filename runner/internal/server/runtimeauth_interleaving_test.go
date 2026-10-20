package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runtimeTraceSQLKey struct{}
type runtimeBindingBarrier struct {
	armed            atomic.Bool
	reached, release chan struct{}
}

func (b *runtimeBindingBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, runtimeTraceSQLKey{}, data.SQL)
}
func (b *runtimeBindingBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(runtimeTraceSQLKey{}).(string)
	if strings.Contains(sql, "UPDATE board_tokens t SET expires_at=now()+interval '24 hours' FROM session_runtime") && b.armed.CompareAndSwap(true, false) {
		close(b.reached)
		<-b.release
	}
}
func TestRuntimeReplacementFencesAlreadyValidatedInboundFrame(t *testing.T) {
	_, hub, host, pool := spawnTokenFixture(t)
	ctx := context.Background()
	const sid = "abcdefab-cdef-4abc-8def-000000000907"
	if err := db.InsertClusterSession(ctx, pool, sid, host.ID, "starting", "/repos/own", "own-project", "", ""); err != nil {
		t.Fatal(err)
	}
	raw, _, err := db.MintRuntimeLaunch(ctx, pool, strings.ToUpper(sid), "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	barrier := &runtimeBindingBarrier{reached: make(chan struct{}), release: make(chan struct{})}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = barrier
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
	if err = conn.WriteJSON(protocol.SessionStarted{Type: "session_started", SessionID: sid, Repo: "own-project", ProjectPath: "/repos/own"}); err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: sid, Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		row, e := db.GetSession(ctx, pool, sid)
		if e != nil {
			t.Fatal(e)
		}
		if row.Status == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	barrier.armed.Store(true)
	if err = conn.WriteJSON(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: []string{sid}, SessionStates: map[string]string{sid: "running"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier.reached:
	case <-time.After(15 * time.Second):
		t.Fatal("inbound frame did not reach binding check")
	}

	mintDone := make(chan error, 1)
	go func() { _, _, e := db.MintRuntimeLaunch(ctx, pool, strings.ToUpper(sid), "", time.Hour); mintDone <- e }()
	select {
	case e := <-mintDone:
		close(barrier.release)
		if e != nil {
			t.Fatal(e)
		}
		t.Fatal("launch rotation crossed an already-validated frame before its effects completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(barrier.release)
	select {
	case e := <-mintDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("rotation did not resume after admitted frame completed")
	}
}

func TestRuntimeReplacedSocketDisconnectCannotMutateNewLaunch(t *testing.T) {
	_, hub, host, pool := spawnTokenFixture(t)
	ctx := context.Background()
	const sid = "00000000-0000-4000-8000-000000000908"
	if err := db.InsertClusterSession(ctx, pool, sid, host.ID, "idle", "/repos/own", "own-project", "", ""); err != nil {
		t.Fatal(err)
	}
	old, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.ValidateRuntimeToken(ctx, pool, old)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := db.RuntimeDaemonID(ctx, pool, sid, token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.UpsertDaemon(ctx, pool, daemon, "runner-"+sid, "runner", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err = db.InsertClusterSession(ctx, pool, sid, daemon, "idle", "/repos/own", "own-project", "", ""); err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: daemon, Name: "runner-" + sid, Mode: "runner", BoundSessionID: sid, RuntimeTokenID: token.ID, send: make(chan []byte, 16)}
	hub.Register(dc)
	hub.SetSessionOwner(sid, daemon)
	current, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handleDaemonDisconnect(hub, dc, pool)
	row, err := db.GetSession(ctx, pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "idle" {
		t.Fatal("obsolete socket disconnect mutated the replacement's status")
	}
	if _, err = db.ValidateRuntimeToken(ctx, pool, current); err != nil {
		t.Fatal("obsolete disconnect revoked replacement authority")
	}
}

func TestRuntimeLaunchFenceDoesNotReserveMainPoolConnection(t *testing.T) {
	_, _, _, pool := spawnTokenFixture(t)
	cfg := pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	single, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = db.WithRuntimeLaunchFence(ctx, single, "abcdefab-cdef-4abc-8def-000000000909", func(fenced context.Context) error {
		var one int
		return single.QueryRow(fenced, `SELECT 1`).Scan(&one)
	})
	if err != nil {
		t.Fatalf("fence prevented callback from using MaxConns=1 main pool: %v", err)
	}
}

func TestRuntimeTerminalSessionRejectsMintAndAdmittedWrites(t *testing.T) {
	for _, terminal := range []string{"stopped", "ended_at"} {
		t.Run(terminal, func(t *testing.T) {
			_, _, host, pool := spawnTokenFixture(t)
			ctx := context.Background()
			const sid = "abcdefab-cdef-4abc-8def-000000000910"
			if err := db.InsertClusterSession(ctx, pool, sid, host.ID, "idle", "/repos/fixture", "fixture", "", ""); err != nil {
				t.Fatal(err)
			}
			raw, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			token, err := db.ValidateRuntimeToken(ctx, pool, raw)
			if err != nil {
				t.Fatal(err)
			}
			scoped := db.WithRuntimeAuthority(ctx, token.ID)
			statement := `UPDATE sessions SET status='stopped' WHERE id=$1`
			if terminal == "ended_at" {
				statement = `UPDATE sessions SET ended_at=now() WHERE id=$1`
			}
			if _, err = pool.Exec(ctx, statement, sid); err != nil {
				t.Fatal(err)
			}
			if _, _, err = db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour); err == nil {
				t.Fatal("mint accepted terminal session")
			}
			var current string
			if err = pool.QueryRow(ctx, `SELECT runtime_token_id FROM session_runtime WHERE session_id=$1`, sid).Scan(&current); err != nil {
				t.Fatal(err)
			}
			if current != token.ID {
				t.Fatal("failed mint changed current launch")
			}
			if err = db.UpdateSessionStatus(scoped, pool, sid, "running", nil); !errors.Is(err, db.ErrTokenInvalid) {
				t.Fatalf("admitted status write: %v", err)
			}
			now := time.Now()
			if err = db.EndSessionStatus(scoped, pool, sid, "error", &now, systemEnd(db.EndReasonProcessFailed)); !errors.Is(err, db.ErrTokenInvalid) {
				t.Fatalf("admitted end write: %v", err)
			}
		})
	}
}

func TestRuntimeCanceledLifecycleDoesNotPublishOrChangeOwnership(t *testing.T) {
	_, hub, host, pool := spawnTokenFixture(t)
	ctx := context.Background()
	const sid = "abcdefab-cdef-4abc-8def-000000000911"
	if err := db.InsertClusterSession(ctx, pool, sid, host.ID, "idle", "/repos/fixture", "fixture", "", ""); err != nil {
		t.Fatal(err)
	}
	raw, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.ValidateRuntimeToken(ctx, pool, raw)
	if err != nil {
		t.Fatal(err)
	}
	send := make(chan []byte, 10)
	hub.RegisterBrowser(&BrowserConn{ID: "runtime-canceled-browser", send: send})
	var completed atomic.Bool
	hub.SetCompletionNotifier(func(string) { completed.Store(true) })
	scoped, cancel := context.WithCancel(db.WithRuntimeAuthority(ctx, token.ID))
	cancel()
	HandleSessionStarted(scoped, hub, pool, host.ID, protocol.SessionStarted{SessionID: sid})
	if hub.FindDaemonForSession(sid) != nil {
		t.Fatal("failed start changed ownership")
	}
	hub.SetSessionOwner(sid, host.ID)
	HandleSessionEnded(scoped, hub, pool, protocol.SessionEnded{SessionID: sid})
	if hub.FindDaemonForSession(sid) != host {
		t.Fatal("failed end removed ownership")
	}
	if completed.Load() {
		t.Fatal("failed end notified completion")
	}
	select {
	case <-send:
		t.Fatal("failed lifecycle published browser state")
	default:
	}
}
