package server

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func joinHeartbeatReplay(t *testing.T, dc *DaemonConn) {
	t.Helper()
	close(dc.desktopDone)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for dc.replayRunning.Load() {
		select {
		case <-deadline.C:
			t.Error("replay did not stop")
			return
		default:
			runtime.Gosched()
		}
	}
}

type rotateAfterCommit struct {
	once   sync.Once
	rotate func() error
	err    error
}
type heartbeatTraceKey struct{}

func (*rotateAfterCommit) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, heartbeatTraceKey{}, strings.EqualFold(strings.TrimSpace(data.SQL), "commit"))
}
func (tr *rotateAfterCommit) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if isCommit, _ := ctx.Value(heartbeatTraceKey{}).(bool); isCommit && data.Err == nil {
		tr.once.Do(func() { tr.err = tr.rotate() })
	}
}

func TestPinnedHeartbeatRotationStopsFurtherStatusWrites(t *testing.T) {
	fx := newLiveFx(t)
	h := fx.api.hub
	gen := newUUID()
	next := newUUID()
	pin := db.DaemonAuthority{DaemonID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, Generation: gen}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, pin.DaemonID, pin.OwnerID, pin.Revision, pin.Generation); err != nil {
		t.Fatal(err)
	}
	second := newUUID()
	if err := db.InsertSession(t.Context(), fx.pool, second, pin.DaemonID, "running", "/fixture", "fixture", "Fixture", ""); err != nil {
		t.Fatal(err)
	}
	ids := []string{fx.sess, second}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='running',spawning_account_id=$2 WHERE id=ANY($1::uuid[])`, ids, pin.OwnerID); err != nil {
		t.Fatal(err)
	}
	ch := make(chan []byte, 20)
	h.RegisterBrowser(&BrowserConn{ID: "fixture-browser", AccountID: pin.OwnerID, send: ch})
	tr := &rotateAfterCommit{rotate: func() error {
		return db.BindDesktopConnection(t.Context(), fx.pool, desktopauth.Binding{DesktopID: pin.DaemonID, OwnerID: pin.OwnerID, Revision: pin.Revision}, "fixture", "host", "", next)
	}}
	cfg := fx.pool.Config().Copy()
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = reconcilePinnedSessions(t.Context(), h, pool, pin, ids, nil, map[string]string{ids[0]: "idle", ids[1]: "idle"})
	if !errors.Is(err, db.ErrDesktopAuthority) || tr.err != nil {
		t.Fatalf("rotation not enforced: reconcile=%v rotate=%v", err, tr.err)
	}
	var changed int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE id=ANY($1::uuid[]) AND status='idle'`, ids).Scan(&changed); err != nil || changed != 1 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	if len(ch) != 1 {
		t.Fatalf("broadcasts=%d want 1 for committed status", len(ch))
	}
}

type endBeforeStatusCAS struct {
	once sync.Once
	end  func() error
	err  error
}

func (tr *endBeforeStatusCAS) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, `SELECT parent_session_id::text FROM sessions`) {
		tr.once.Do(func() { tr.err = tr.end() })
	}
	return ctx
}
func (*endBeforeStatusCAS) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestPinnedHeartbeatConcurrentStopIsNotRevivedOrBroadcast(t *testing.T) {
	fx := newLiveFx(t)
	h := fx.api.hub
	gen := newUUID()
	pin := db.DaemonAuthority{DaemonID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, Generation: gen}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, pin.DaemonID, pin.OwnerID, pin.Revision, gen); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='error',spawning_account_id=$2 WHERE id=$1`, fx.sess, pin.OwnerID); err != nil {
		t.Fatal(err)
	}
	ch := make(chan []byte, 10)
	h.RegisterBrowser(&BrowserConn{ID: "fixture-browser", AccountID: pin.OwnerID, send: ch})
	tr := &endBeforeStatusCAS{end: func() error {
		now := time.Now()
		return db.EndSessionStatus(t.Context(), fx.pool, fx.sess, "stopped", &now, db.SessionEnd{Reason: db.EndReasonStoppedByUser})
	}}
	cfg := fx.pool.Config().Copy()
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	stopped, err := reconcilePinnedSessions(t.Context(), h, pool, pin, []string{fx.sess}, nil, nil)
	if err != nil || tr.err != nil || len(stopped) != 0 || len(ch) != 0 {
		t.Fatalf("stale reconcile: stopped=%v err=%v stop=%v broadcasts=%d", stopped, err, tr.err, len(ch))
	}
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.Status != "stopped" {
		t.Fatalf("stop overwritten: %+v %v", row, err)
	}
	stopped, err = reconcilePinnedSessions(t.Context(), h, pool, pin, []string{fx.sess}, nil, nil)
	if err != nil || len(stopped) != 1 || stopped[0] != fx.sess {
		t.Fatalf("stop not retried: %v %v", stopped, err)
	}
}

type blockBackgroundQuery struct {
	entered, release chan struct{}
	once             sync.Once
}

func (tr *blockBackgroundQuery) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, `SELECT id FROM sessions WHERE daemon_id=`) {
		tr.once.Do(func() { close(tr.entered); <-tr.release })
	}
	return ctx
}
func (*blockBackgroundQuery) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestPinnedHeartbeatSlowPhaseTwoReleasesDesktopFence(t *testing.T) {
	fx := newLiveFx(t)
	h := fx.api.hub
	generation := newUUID()
	binding := desktopauth.Binding{DesktopID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, ExpiresAt: time.Now().Add(time.Hour)}
	dc := &DaemonConn{ID: topicDaemonID, DesktopBinding: &binding, DesktopGeneration: generation, BackgroundTaskLifetime: newUUID(), send: make(chan []byte, 8), desktopDone: make(chan struct{})}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, dc.ID, binding.OwnerID, binding.Revision, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, fx.sess, binding.OwnerID); err != nil {
		t.Fatal(err)
	}
	h.Register(dc)
	tracer := &blockBackgroundQuery{entered: make(chan struct{}), release: make(chan struct{})}
	cfg := fx.pool.Config().Copy()
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { joinHeartbeatReplay(t, dc); pool.Close() }()
	hb := protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: []string{fx.sess}, BackgroundTasks: &protocol.BackgroundTaskInventory{Lifetime: dc.BackgroundTaskLifetime, Sessions: map[string][]string{}}}
	raw, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	var after func()
	if err := db.WithDesktopFence(t.Context(), pool, dc.ID, func(ctx context.Context) error {
		if !handleDaemonFrame(ctx, h, dc, pool, raw, envelope{Type: "daemon_heartbeat"}, func(work func()) { after = work }) {
			return errors.New("frame refused")
		}
		return nil
	}); err != nil || after == nil {
		t.Fatalf("heartbeat admission: %v", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); after() }()
	defer func() { close(tracer.release); <-done }()
	select {
	case <-tracer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("phase two did not start")
	}
	probe, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := db.WithDesktopFence(probe, fx.pool, dc.ID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("phase two held authority fence: %v", err)
	}
	t.Logf("fence during blocked reconciliation: %s", time.Since(start))
}

func TestPinnedHeartbeatStaleGenerationCannotSendStoppedKill(t *testing.T) {
	fx := newLiveFx(t)
	gen := newUUID()
	pin := db.DaemonAuthority{DaemonID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, Generation: gen}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, pin.DaemonID, pin.OwnerID, pin.Revision, newUUID()); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='stopped',end_reason='stopped_by_user',spawning_account_id=$2 WHERE id=$1`, fx.sess, pin.OwnerID); err != nil {
		t.Fatal(err)
	}
	stopped, err := reconcilePinnedSessions(t.Context(), fx.api.hub, fx.pool, pin, []string{fx.sess}, nil, nil)
	if !errors.Is(err, db.ErrDesktopAuthority) || len(stopped) != 0 {
		t.Fatalf("stale generation could send kill: %v %v", stopped, err)
	}
}

func TestPinnedStatusFrameDoesNotHoldFenceOnBlockedSession(t *testing.T) {
	fx := newLiveFx(t)
	h := fx.api.hub
	gen := newUUID()
	binding := desktopauth.Binding{DesktopID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, ExpiresAt: time.Now().Add(time.Hour)}
	dc := &DaemonConn{ID: topicDaemonID, DesktopBinding: &binding, DesktopGeneration: gen, send: make(chan []byte, 8)}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, dc.ID, binding.OwnerID, binding.Revision, gen); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='running',spawning_account_id=$2 WHERE id=$1`, fx.sess, binding.OwnerID); err != nil {
		t.Fatal(err)
	}
	h.Register(dc)
	tx, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, fx.sess); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan struct{})
	raw, err := json.Marshal(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: fx.sess, Status: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(done)
		var after func()
		_ = db.WithDesktopFence(ctx, fx.pool, dc.ID, func(fenced context.Context) error {
			handleDaemonFrame(fenced, h, dc, fx.pool, raw, envelope{Type: "session_state_changed"}, func(work func()) { after = work })
			return nil
		})
		if after != nil {
			after()
		}
	}()
	defer func() { _ = tx.Rollback(context.Background()); cancel(); <-done }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := fx.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, tx.Conn().PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("status did not block on row")
		}
	}
	probe, stop := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer stop()
	start := time.Now()
	if err := db.WithDesktopFence(probe, fx.pool, dc.ID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("blocked status retained authority fence after %s: %v", time.Since(start), err)
	}
	t.Logf("fence during blocked status frame: %s", time.Since(start))
	hbRaw, err := json.Marshal(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: []string{fx.sess}})
	if err != nil {
		t.Fatal(err)
	}
	heartbeatCtx, stopHeartbeat := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer stopHeartbeat()
	heartbeatStart := time.Now()
	err = db.WithDesktopFence(heartbeatCtx, fx.pool, dc.ID, func(fenced context.Context) error {
		if !handleDaemonFrame(fenced, h, dc, fx.pool, hbRaw, envelope{Type: "daemon_heartbeat"}, func(func()) {}) {
			return context.Canceled
		}
		return fenced.Err()
	})
	if err != nil {
		t.Fatalf("heartbeat waited on blocked status after %s: %v", time.Since(heartbeatStart), err)
	}
	t.Logf("heartbeat admission during blocked status: %s", time.Since(heartbeatStart))
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-done
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.Status != "idle" {
		t.Fatalf("status not applied after row release: %+v %v", row, err)
	}
}

func TestPinnedStatusFramesApplyInReadPumpOrder(t *testing.T) {
	fx := newLiveFx(t)
	h := fx.api.hub
	gen := newUUID()
	binding := desktopauth.Binding{DesktopID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, ExpiresAt: time.Now().Add(time.Hour)}
	dc := &DaemonConn{ID: topicDaemonID, DesktopBinding: &binding, DesktopGeneration: gen, send: make(chan []byte, 8)}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, dc.ID, binding.OwnerID, binding.Revision, gen); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='running',spawning_account_id=$2 WHERE id=$1`, fx.sess, binding.OwnerID); err != nil {
		t.Fatal(err)
	}
	h.Register(dc)
	for _, status := range []string{"idle", "running"} {
		raw, err := json.Marshal(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: fx.sess, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		var after func()
		if err := db.WithDesktopFence(t.Context(), fx.pool, dc.ID, func(ctx context.Context) error {
			handleDaemonFrame(ctx, h, dc, fx.pool, raw, envelope{Type: "session_state_changed"}, func(work func()) { after = work })
			return nil
		}); err != nil || after == nil {
			t.Fatalf("status admission: %v", err)
		}
		after()
	}
	var got []string
	rows, err := fx.pool.Query(t.Context(), `SELECT data::jsonb->>'status' FROM session_events WHERE session_id=$1 AND type='state_change' ORDER BY id`, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		got = append(got, status)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(got) != 2 || got[0] != "idle" || got[1] != "running" {
		t.Fatalf("state events=%v err=%v", got, err)
	}
}

func TestPinnedStatusRetryExhaustionHeartbeatConvergesLiveStatusOnly(t *testing.T) {
	fx := newLiveFx(t)
	gen := newUUID()
	pin := db.DaemonAuthority{DaemonID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, Generation: gen}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, pin.DaemonID, pin.OwnerID, pin.Revision, gen); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='running',unread=false,spawning_account_id=$2 WHERE id=$1`, fx.sess, pin.OwnerID); err != nil {
		t.Fatal(err)
	}
	tx, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, fx.sess); err != nil {
		t.Fatal(err)
	}
	detail := "one-shot-detail"
	applyPinnedStateFrame(t.Context(), fx.api.hub, fx.pool, pin, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: fx.sess, Status: "idle", Message: &detail})
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.Status != "running" {
		t.Fatalf("blocked frame unexpectedly applied: %+v %v", row, err)
	}
	_, err = reconcilePinnedSessions(t.Context(), fx.api.hub, fx.pool, pin, []string{fx.sess}, nil, map[string]string{fx.sess: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	row, err = db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.Status != "idle" || row.Unread {
		t.Fatalf("heartbeat rederives status but not one-shot unread: %+v %v", row, err)
	}
	var detailEvents int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_events WHERE session_id=$1 AND data LIKE '%one-shot-detail%'`, fx.sess).Scan(&detailEvents); err != nil || detailEvents != 0 {
		t.Fatalf("one-shot detail unexpectedly reconstructed: %d %v", detailEvents, err)
	}
}

func TestPinnedStatusFrameRejectsRotatedGeneration(t *testing.T) {
	fx := newLiveFx(t)
	gen := newUUID()
	pin := db.DaemonAuthority{DaemonID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, Generation: gen}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, pin.DaemonID, pin.OwnerID, pin.Revision, newUUID()); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='running',spawning_account_id=$2 WHERE id=$1`, fx.sess, pin.OwnerID); err != nil {
		t.Fatal(err)
	}
	applyPinnedStateFrame(t.Context(), fx.api.hub, fx.pool, pin, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: fx.sess, Status: "idle"})
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.Status != "running" {
		t.Fatalf("stale frame changed status: %+v %v", row, err)
	}
	var count int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_events WHERE session_id=$1 AND type='state_change'`, fx.sess).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale frame event count=%d err=%v", count, err)
	}
}

func TestPinnedTerminalStatusFrameRevokesAndBroadcastsOnce(t *testing.T) {
	fx := newLiveFx(t)
	h := fx.api.hub
	var terminal, child int
	h.SetDelegationHooks(func(string) { terminal++ }, nil)
	h.SetChildTurnHook(func(_ string, kind, _ string) {
		if kind == "recommended_channel_closed" {
			child++
		}
	})
	gen := newUUID()
	pin := db.DaemonAuthority{DaemonID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, Generation: gen}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, pin.DaemonID, pin.OwnerID, pin.Revision, gen); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='running',spawning_account_id=$2 WHERE id=$1`, fx.sess, pin.OwnerID); err != nil {
		t.Fatal(err)
	}
	board, err := db.CreateBoard(t.Context(), fx.pool, "fixture", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := db.MintBoardToken(t.Context(), fx.pool, board.ID, fx.sess, []string{"read"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan []byte, 10)
	h.RegisterBrowser(&BrowserConn{ID: "fixture-browser", AccountID: pin.OwnerID, send: ch})
	applyPinnedStateFrame(t.Context(), h, fx.pool, pin, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: fx.sess, Status: "stopped"})
	var revoked bool
	if err := fx.pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM board_tokens WHERE id=$1`, token.ID).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("terminal token revoke=%v err=%v", revoked, err)
	}
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.Status != "stopped" {
		t.Fatalf("terminal status %+v err=%v", row, err)
	}
	var count int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_events WHERE session_id=$1 AND type='state_change'`, fx.sess).Scan(&count); err != nil || count != 1 {
		t.Fatalf("terminal events=%d err=%v", count, err)
	}
	if len(ch) != 1 || terminal != 1 || child != 1 {
		t.Fatalf("terminal broadcasts=%d terminal=%d child=%d", len(ch), terminal, child)
	}
}

func TestPinnedMissingSessionErrorBroadcastsAfterAuthorityValidation(t *testing.T) {
	fx := newLiveFx(t)
	h := fx.api.hub
	gen := newUUID()
	pin := db.DaemonAuthority{DaemonID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, Generation: gen}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, pin.DaemonID, pin.OwnerID, pin.Revision, gen); err != nil {
		t.Fatal(err)
	}
	missing := newUUID()
	ch := make(chan []byte, 10)
	h.RegisterBrowser(&BrowserConn{ID: "fixture-browser", AccountID: pin.OwnerID, send: ch})
	applyPinnedStateFrame(t.Context(), h, fx.pool, pin, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: missing, Status: "error"})
	if len(ch) != 1 {
		t.Fatalf("pre-start error broadcasts=%d", len(ch))
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET connection_generation=$2 WHERE id=$1`, pin.DaemonID, newUUID()); err != nil {
		t.Fatal(err)
	}
	applyPinnedStateFrame(t.Context(), h, fx.pool, pin, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: missing, Status: "error"})
	if len(ch) != 1 {
		t.Fatalf("stale generation broadcast pre-start error: broadcasts=%d", len(ch))
	}
}

func TestPinnedHeartbeatStatusBroadcastRespectsSessionOwner(t *testing.T) {
	f := newPrivFixture(t)
	owner := &BrowserConn{ID: "pinned-owner", AccountID: privAcctA, send: make(chan []byte, 8)}
	foreign := &BrowserConn{ID: "pinned-foreign", AccountID: privAcctB, send: make(chan []byte, 8)}
	f.hub.RegisterBrowser(owner)
	f.hub.RegisterBrowser(foreign)
	check := func(status string) {
		t.Helper()
		publishPinnedSessionStatus(t.Context(), f.hub, f.pool, f.privSession, status, false)
		if len(owner.send) != 1 {
			t.Fatalf("owner received %d status messages", len(owner.send))
		}
		var msg protocol.SessionStateChanged
		if err := json.Unmarshal(<-owner.send, &msg); err != nil || msg.SessionID != f.privSession || msg.Status != status {
			t.Fatalf("invalid owner message: %+v, %v", msg, err)
		}
		if len(foreign.send) != 0 {
			t.Fatal("foreign account received private session status")
		}
	}
	check("idle")
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), f.pool, f.privSession, "stopped", &now, db.SessionEnd{Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman, ByAccount: privAcctA}); err != nil {
		t.Fatal(err)
	}
	check("stopped")
}

type backgroundRowAttempt struct {
	once      sync.Once
	attempted chan struct{}
}

func (tr *backgroundRowAttempt) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, heartbeatTraceKey{}, strings.Contains(data.SQL, "SELECT daemon_id,status,ended_at,background_tasks"))
}
func (tr *backgroundRowAttempt) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if attempted, _ := ctx.Value(heartbeatTraceKey{}).(bool); attempted {
		tr.once.Do(func() { close(tr.attempted) })
	}
}
func TestHelloBackgroundReconcileRetriesLockedSession(t *testing.T) {
	fx := newLiveFx(t)
	gen, lifetime := newUUID(), newUUID()
	pin := db.DaemonAuthority{DaemonID: topicDaemonID, OwnerID: "fixture-owner", Revision: 3, Generation: gen}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=$3,connection_generation=$4 WHERE id=$1`, pin.DaemonID, pin.OwnerID, pin.Revision, gen); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, fx.sess, pin.OwnerID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), fx.pool, fx.sess, newUUID(), "background_task_started", `{"task_id":"orphan","daemon_lifetime":"old"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	initial, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || initial.BackgroundTasks != 1 {
		t.Fatalf("orphan setup count: %+v %v", initial, err)
	}
	tx, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, fx.sess); err != nil {
		t.Fatal(err)
	}
	tr := &backgroundRowAttempt{attempted: make(chan struct{})}
	cfg := fx.pool.Config().Copy()
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	dc := &DaemonConn{ID: pin.DaemonID, DesktopBinding: &desktopauth.Binding{DesktopID: pin.DaemonID, OwnerID: pin.OwnerID, Revision: pin.Revision}, DesktopGeneration: gen, BackgroundTaskLifetime: lifetime}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- reconcilePinnedBackgroundHello(ctx, fx.api.hub, pool, dc, pin, &protocol.BackgroundTaskInventory{Lifetime: lifetime, Sessions: map[string][]string{}})
	}()
	select {
	case <-tr.attempted:
	case <-ctx.Done():
		t.Fatal("reconcile never attempted locked row")
	}
	select {
	case err := <-done:
		t.Fatalf("reconciliation returned without retrying locked session: %v", err)
	default:
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.BackgroundTasks != 0 {
		t.Fatalf("orphan not retried after lock: row=%+v err=%v", row, err)
	}
}
