package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestBLG768HeartbeatReplayDoesNotHoldDesktopFence(t *testing.T) {
	for _, mode := range []string{"delivery", "database"} {
		t.Run(mode, func(t *testing.T) { testBLG768HeartbeatReplay(t, mode) })
	}
}
func testBLG768HeartbeatReplay(t *testing.T, mode string) {
	f := newPrivFixture(t)
	token := "bdt_" + strings.Repeat("a", 64)
	id := newUUID()
	b := desktopauth.Binding{DesktopID: id, OwnerID: privAcctA, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	desktopFakeCore(t, f.api, map[string]desktopauth.Binding{token: b})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.hub.planInputContext = func(_ context.Context, _ *DaemonConn, raw []byte) ([]byte, func(bool)) {
		var msg protocol.AgentUserMessage
		_ = json.Unmarshal(raw, &msg)
		if mode == "delivery" && msg.ClientID == "slow-replay" {
			close(entered)
			<-release
			close(done)
		}
		return raw, nil
	}
	pool := f.pool
	if mode == "database" {
		cfg := f.pool.Config().Copy()
		cfg.ConnConfig.Tracer = &slowReplayQuery{entered: entered, release: release, done: done}
		var err error
		pool, err = pgxpool.NewWithConfig(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", f.hub.ServeDaemon(spawnTokenDaemonTok, pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	peer := desktopConnect(t, srv.URL, token, id, "slow-replay", true)
	defer peer.Close()
	dc := desktopWait(t, f.hub, id)
	dc.SetInputReceipts(true)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$1 WHERE id IN ($2,$3)`, id, f.pubSession, f.privSession); err != nil {
		t.Fatal(err)
	}
	f.hub.SetSessionOwner(f.pubSession, id)
	f.hub.SetSessionOwner(f.privSession, id)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.pubSession, ClientID: "slow-replay", Text: "continue"}
	if _, err := acceptSessionInput(t.Context(), f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteJSON(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: []string{f.pubSession, f.privSession}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("replay did not enter delivery")
	}
	defer func() {
		close(release)
		<-done
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for dc.replayRunning.Load() {
			select {
			case <-deadline.C:
				t.Error("replay worker did not finish")
				return
			default:
				runtime.Gosched()
			}
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := db.WithDesktopFence(ctx, f.pool, id, func(context.Context) error { return nil }); err != nil {
		t.Errorf("slow replay blocks other desktop frames: %v", err)
	}
	event := protocol.AgentEvent{Type: "agent_event", SessionID: f.privSession, ClientEventID: newUUID(), Kind: "assistant_text", Payload: json.RawMessage(`{"text":"other session"}`)}
	if err := peer.WriteJSON(event); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	for {
		_, raw, err := peer.ReadMessage()
		if err != nil {
			t.Fatalf("slow replay blocks socket reader: %v", err)
		}
		if strings.Contains(string(raw), event.ClientEventID) {
			break
		}
	}
}
func TestBLG768GrantDeliveryReleasesSessionRowBeforeSend(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "grant-state:test:1", Source: "system", Text: "permission suspended"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	f.hub.planInputContext = func(_ context.Context, _ *DaemonConn, raw []byte) ([]byte, func(bool)) {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		if _, err := f.pool.Exec(ctx, `UPDATE sessions SET title=title WHERE id=$1`, f.sessionID); err != nil {
			t.Errorf("notice delivery retains session row lock: %v", err)
		}
		return raw, nil
	}
	raw, _ := json.Marshal(msg)
	if sent, err := sendGrantAuthorityNotice(f.ctx, f.hub, f.pool, f.dc, msg, raw); err != nil || !sent {
		t.Fatalf("send = %v, %v", sent, err)
	}
}

func TestBLG768ReplayBoundsInputBatch(t *testing.T) {
	f := newIngestFixture(t)
	for i := 0; i < 20; i++ {
		msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: fmt.Sprintf("batch-%d", i), Text: "continue"}
		if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
			t.Fatal(err)
		}
	}
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	if got := len(f.dc.send); got != 16 {
		t.Fatalf("replay sent %d frames; want bounded batch of 16", got)
	}
	pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(pending) != 20 {
		t.Fatalf("batch must retain every input until receipt: %d %v", len(pending), err)
	}
}

// A transport seam blocks only the replay query, never authority validation or
// another session's frame. The real pgx client and database handle every query.
type slowReplayQuery struct {
	entered, release, done chan struct{}
	once                   sync.Once
}

func (q *slowReplayQuery) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "SELECT EXISTS(SELECT 1 FROM session_inputs") {
		q.once.Do(func() { close(q.entered); <-q.release; close(q.done) })
	}
	return ctx
}
func (*slowReplayQuery) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestBLG768SupersededGrantNoticeIsNotReplayed(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "grant-state:test:1", Source: "system", Text: "permission suspended"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	if err := db.SettleSessionInput(f.ctx, f.pool, f.sessionID, msg.ClientID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(msg)
	if handled, err := sendGrantAuthorityNotice(f.ctx, f.hub, f.pool, f.dc, msg, raw); err != nil || !handled {
		t.Fatalf("superseded notice = %v, %v", handled, err)
	}
	if len(f.dc.send) != 0 {
		t.Fatal("superseded notice delivered")
	}
}

func TestBLG768PlanPreparationHonorsReplayDeadline(t *testing.T) {
	f := newPrivFixture(t)
	f.dc.planNudges = true
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err = tx.Exec(t.Context(), `LOCK TABLE session_plan_deliveries IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.pubSession, ClientID: "blocked-preparation", Text: "continue"})
	if f.hub.trySendToDaemonContext(ctx, f.dc, raw) {
		t.Fatal("replay queued after preparation exceeded its deadline")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("expected database deadline, got %v", ctx.Err())
	}
	if len(f.dc.send) != 0 {
		t.Fatal("expired replay reached daemon")
	}
}
