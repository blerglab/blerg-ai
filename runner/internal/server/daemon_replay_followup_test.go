package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func waitReplayWorker(t *testing.T, dc *DaemonConn) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for dc.replayRunning.Load() {
		select {
		case <-deadline.C:
			t.Fatal("replay worker did not finish")
		default:
			runtime.Gosched()
		}
	}
}

func fortyReplaySessions(t *testing.T, f *ingestFixture) []string {
	t.Helper()
	ids := []string{f.sessionID}
	for i := 1; i < 40; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012x", i)
		if err := db.InsertSession(f.ctx, f.pool, id, f.dc.ID, "idle", "/fixture", "fixture", "fixture", ""); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestBLG768FailedLiveSendReplaysOnNextHeartbeat(t *testing.T) {
	f := newIngestFixture(t)
	ids := fortyReplaySessions(t, f)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "failed-live-send", Text: "continue"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(msg)
	for len(f.dc.send) < cap(f.dc.send) {
		f.dc.send <- []byte(`{}`)
	}
	if f.hub.trySendToDaemonContext(f.ctx, f.dc, raw) {
		t.Fatal("live send unexpectedly succeeded")
	}
	for len(f.dc.send) > 0 {
		<-f.dc.send
	}
	heartbeat, _ := json.Marshal(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: ids})
	if !handleDaemonFrame(f.ctx, f.hub, f.dc, f.pool, heartbeat, envelope{Type: "daemon_heartbeat"}) {
		t.Fatal("heartbeat rejected")
	}
	defer waitReplayWorker(t, f.dc)
	select {
	case frame := <-f.dc.send:
		var got protocol.AgentUserMessage
		if err := json.Unmarshal(frame, &got); err != nil || got.ClientID != msg.ClientID {
			t.Fatalf("replayed frame = %+v, %v", got, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("person's pending input at the end of a 40-session desktop missed the next heartbeat")
	}
	waitReplayWorker(t, f.dc)
	if got := f.dc.replayCursor.Load(); got <= 8 {
		t.Fatalf("worker stopped after %d sessions with work budget available", got)
	}
}

func TestBLG768ReplayDropsAreLoggedAndRateLimited(t *testing.T) {
	var output bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&output, nil)))
	defer func() { failurelog.Default = previous }()
	h := NewHub()
	dc := &DaemonConn{ID: "fixture"}
	pool := lazyPool(t)
	dc.replayRunning.Store(true)
	for range 20 {
		scheduleDaemonReplay(t.Context(), h, pool, dc, nil)
	}
	dc.replayRunning.Store(false)
	for range cap(daemonReplaySlots) {
		daemonReplaySlots <- struct{}{}
	}
	defer func() {
		for range cap(daemonReplaySlots) {
			<-daemonReplaySlots
		}
	}()
	for range 20 {
		scheduleDaemonReplay(t.Context(), h, pool, dc, nil)
	}
	for _, cause := range []string{"already_running", "slots_full"} {
		if got := strings.Count(output.String(), `"cause":"`+cause+`"`); got != 1 {
			t.Errorf("cause %s logged %d times; want one rate-limited event", cause, got)
		}
	}
	if dc.replayRunning.Load() {
		t.Fatal("slot rejection retained worker claim")
	}
}

func TestBLG768ReplayPanicIsContained(t *testing.T) {
	if os.Getenv("BLG_REPLAY_PANIC_PROBE") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestBLG768ReplayPanicIsContained$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), "BLG_REPLAY_PANIC_PROBE=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("replay panic escaped worker: %v; child reported panic=%v", err, bytes.Contains(output, []byte("panic:")))
		}
		return
	}
	f := newIngestFixture(t)
	var output bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&output, nil)))
	defer func() { failurelog.Default = previous }()
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "panic-replay", Text: "continue"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	f.hub.planInputContext = func(context.Context, *DaemonConn, []byte) ([]byte, func(bool)) { panic("fixture-private-panic-canary") }
	scheduleDaemonReplay(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	waitReplayWorker(t, f.dc)
	if len(daemonReplaySlots) != 0 {
		t.Fatal("panic leaked replay slot")
	}
	if !strings.Contains(output.String(), `"cause":"panic"`) {
		t.Fatal("worker panic was not logged")
	}
	if strings.Contains(output.String(), "fixture-private-panic-canary") {
		t.Fatal("panic value leaked into diagnostics")
	}
	f.hub.planInputContext = nil
	scheduleDaemonReplay(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	waitReplayWorker(t, f.dc)
	if len(f.dc.send) != 1 {
		t.Fatal("panic prevented later replay")
	}
}

func TestBLG768DeadTokenIsPrioritizedOnNextHeartbeat(t *testing.T) {
	f := newIngestFixture(t)
	ids := fortyReplaySessions(t, f)
	for _, id := range ids {
		if id == f.sessionID {
			continue
		}
		if _, err := db.MintSessionToken(f.ctx, f.pool, id, []string{"message"}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	f.dc.liveMu.Lock()
	f.dc.sessionTokenFile = true
	f.dc.liveMu.Unlock()
	heartbeat, _ := json.Marshal(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: ids})
	if !handleDaemonFrame(f.ctx, f.hub, f.dc, f.pool, heartbeat, envelope{Type: "daemon_heartbeat"}) {
		t.Fatal("heartbeat rejected")
	}
	defer waitReplayWorker(t, f.dc)
	select {
	case raw := <-f.dc.send:
		var got protocol.SessionToken
		if err := json.Unmarshal(raw, &got); err != nil || got.Type != "session_token" || got.SessionID != f.sessionID {
			t.Fatalf("token repair = %+v, %v", got, err)
		}
		handleSessionTokenResult(f.ctx, f.hub, f.pool, protocol.SessionTokenResult{SessionID: f.sessionID, OK: true})
	case <-time.After(2 * time.Second):
		t.Fatal("dead token at end of inventory was not prioritized")
	}
}
