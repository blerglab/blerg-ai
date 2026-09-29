package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/gorilla/websocket"
)

// wsSetup spins up a test server plus a gated board and an agent token
// scoped to it, ready to open a /ws connection against.
func wsSetup(t *testing.T) (srv *httptest.Server, boardID, token string) {
	t.Helper()
	srv, pool := testServer(t)
	ctx := context.Background()
	gt, f := true, false
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "ws-board", GateEnabled: &gt, RequireRepo: &f,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := db.MintToken(ctx, pool, &board.ID, "agent", "ws-tester",
		[]string{"card.read"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return srv, board.ID, raw
}

func dialWS(t *testing.T, srv *httptest.Server, boardID, token string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?board=" + boardID
	header := http.Header{"Authorization": {"Bearer " + token}}
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestHubSendsPeriodicPings pins down the actual keepalive behavior: with a
// client that pumps its read loop (so control frames get dispatched) but is
// otherwise idle, the hub must still push a ping within one ping period.
func TestHubSendsPeriodicPings(t *testing.T) {
	restore := api.SetKeepaliveIntervals(30*time.Millisecond, 500*time.Millisecond)
	defer restore()

	srv, boardID, token := wsSetup(t)
	conn := dialWS(t, srv, boardID, token)

	pinged := make(chan struct{}, 1)
	conn.SetPingHandler(func(appData string) error {
		select {
		case pinged <- struct{}{}:
		default:
		}
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
	})
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-pinged:
	case <-time.After(2 * time.Second):
		t.Fatal("hub did not send a ping within 2s of connecting")
	}
}

// TestHubClosesUnresponsiveConnections is the point of the card: a socket
// that stops answering pings (an idle mobile NAT silently dropping the
// stream) must be torn down by the server instead of held open until the
// 3600s ingress timeout.
func TestHubClosesUnresponsiveConnections(t *testing.T) {
	restore := api.SetKeepaliveIntervals(20*time.Millisecond, 80*time.Millisecond)
	defer restore()

	srv, boardID, token := wsSetup(t)
	conn := dialWS(t, srv, boardID, token)

	// Simulate a client that has gone dark: it still pumps ReadMessage (so
	// gorilla dispatches the incoming ping control frame at all) but never
	// answers with a pong.
	conn.SetPingHandler(func(string) error { return nil })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hub did not close a connection that stopped answering pings")
	}
}

// TestHubBroadcastStillWorks is a regression check for the wsConn refactor:
// Broadcast and the ping ticker share one write path now, so a normal
// broadcast message must still be delivered.
func TestHubBroadcastStillWorks(t *testing.T) {
	restore := api.SetKeepaliveIntervals(time.Hour, time.Hour)
	defer restore()

	srv, boardID, token := wsSetup(t)
	conn := dialWS(t, srv, boardID, token)

	// Give the reader goroutine a moment to register the conn before we
	// trigger a broadcast from outside the WS handshake path.
	time.Sleep(50 * time.Millisecond)
	srvAPI.Hub.Broadcast(boardID, "card_changed")

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected broadcast message, got error: %v", err)
	}
	if !strings.Contains(string(msg), "card_changed") {
		t.Fatalf("unexpected broadcast payload: %s", msg)
	}
}
