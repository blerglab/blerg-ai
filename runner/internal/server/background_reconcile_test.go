package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// Use the real hello socket and a subsequent durable event acknowledgement as a
// barrier: reconciliation must finish before ordinary frames can be ingested.
func backgroundHelloBarrier(t *testing.T, fx *topicFx, srvURL string, inventory any) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srvURL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello := map[string]any{"type": "daemon_hello", "name": "laptop", "mode": "local", "token": topicDaemonToken, "active_sessions": []string{fx.sess}, "protocol_version": "1"}
	if inventory != nil {
		hello["background_task_inventory"] = inventory
	}
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	id := newUUID()
	if err := conn.WriteJSON(protocol.AgentEvent{Type: "agent_event", SessionID: fx.sess, ClientEventID: id, Kind: "fixture_barrier", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		var ack protocol.AgentEventAck
		if err := conn.ReadJSON(&ack); err != nil {
			t.Fatal(err)
		}
		if ack.Type == "agent_event_ack" && ack.ClientEventID == id {
			return
		}
	}
}

func TestBackgroundReconcileHelloClosesOrphanAcrossTwoRestarts(t *testing.T) {
	fx := newLiveFx(t)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 16)}
	HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, dc, protocol.AgentEvent{Type: "agent_event", SessionID: fx.sess, ClientEventID: newUUID(), Kind: "background_task_started", Payload: json.RawMessage(`{"task_id":"orphan","call_id":"call","kind":"bash"}`)})
	srv := httptest.NewServer(fx.api.hub.ServeDaemon(topicDaemonToken, fx.pool))
	defer srv.Close()
	for _, life := range []string{"00000000-0000-4000-8000-000000000091", "00000000-0000-4000-8000-000000000092"} {
		backgroundHelloBarrier(t, fx, srv.URL, map[string]any{"lifetime": life, "sessions": map[string][]string{}})
		row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
		if err != nil || row.BackgroundTasks != 0 {
			t.Fatalf("orphan survived restart: count=%v err=%v", row, err)
		}
	}
	var n int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='background_task_done' AND payload->>'status'='stopped'`, fx.sess).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stopped events=%d err=%v", n, err)
	}
	live := decodeLive(t, fx.getLive(fx.sess, fx.token(topicOwner, "human"), ""))
	if len(live.Tasks) != 0 {
		t.Fatalf("orphan still in live panel: %+v", live.Tasks)
	}
}

func TestBackgroundReconcileHelloPreservesUnknownAndContinuingTasks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inventory any
	}{
		{"old daemon", nil},
		{"continuing", map[string]any{"lifetime": "00000000-0000-4000-8000-000000000093", "sessions": map[string][]string{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newLiveFx(t)
			if inv, ok := tc.inventory.(map[string]any); ok {
				inv["sessions"] = map[string][]string{fx.sess: {"alive"}}
			}
			dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
			HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, dc, protocol.AgentEvent{Type: "agent_event", SessionID: fx.sess, ClientEventID: newUUID(), Kind: "background_task_started", Payload: json.RawMessage(`{"task_id":"alive","call_id":"call","kind":"monitor"}`)})
			srv := httptest.NewServer(fx.api.hub.ServeDaemon(topicDaemonToken, fx.pool))
			defer srv.Close()
			backgroundHelloBarrier(t, fx, srv.URL, tc.inventory)
			row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
			if err != nil || row.BackgroundTasks != 1 {
				t.Fatalf("live task incorrectly closed: row=%+v err=%v", row, err)
			}
		})
	}
}
