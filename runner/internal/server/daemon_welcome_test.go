package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// readFirstMessage reads one frame from conn, failing at the readiness deadline.
func readFirstMessage(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no message from the server: %v", err)
	}
	return raw
}

// A daemon whose hello says it understands daemon_welcome is told, after the token is accepted,
// which daemon it is: the id and the name the server keeps. Nothing secret rides along.
func TestDaemonWelcomeSentWhenTheHelloAsks(t *testing.T) {
	hub := NewHub()
	conn := dialDaemon(t, hub)
	if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "Work-Laptop", Token: "tok-1234567890",
		ProtocolVersion: "1", Welcome: true}); err != nil {
		t.Fatal(err)
	}
	raw := readFirstMessage(t, conn)
	var w protocol.DaemonWelcome
	if err := json.Unmarshal(raw, &w); err != nil || w.Type != "daemon_welcome" {
		t.Fatalf("first message = %s, want daemon_welcome", raw)
	}
	if w.WorkspaceCapabilities != (protocol.WorkspaceCapabilities{}) {
		t.Fatal("unverified workspace capability escaped on the authenticated welcome")
	}
	if w.DaemonID == "" || w.Name != "work-laptop" {
		t.Errorf("welcome = %+v, want the daemon id and the normalised name", w)
	}
	if w.SessionID != "" {
		t.Errorf("an ordinary daemon's welcome carries session_id %q", w.SessionID)
	}
	if strings.Contains(string(raw), "tok-1234567890") {
		t.Fatal("the welcome echoes the daemon token")
	}
}

// A 0.2.0 daemon logs "unhandled message type" for anything it does not know, so a daemon whose
// hello does not ask for the welcome never gets one.
func TestDaemonWelcomeNotSentToAnOlderDaemon(t *testing.T) {
	hub := NewHub()
	conn := dialDaemon(t, hub)
	if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "old-box", Token: "tok-1234567890",
		ProtocolVersion: "1"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for len(hub.GetAllDaemons()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the daemon did not register")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A message queued after attach is the first one the daemon sees: nothing came before it.
	hub.GetAllDaemons()[0].send <- []byte(`{"type":"sentinel"}`)
	raw := readFirstMessage(t, conn)
	if !strings.Contains(string(raw), `"sentinel"`) {
		t.Fatalf("first message to a daemon that did not ask for a welcome = %s", raw)
	}
}

// A refused token gets the 4401 close and no welcome.
func TestDaemonWelcomeNotSentOnARefusedToken(t *testing.T) {
	hub := NewHub()
	conn := dialDaemon(t, hub)
	if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "box", Token: "wrong-token-000000",
		ProtocolVersion: "1", Welcome: true}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("a refused daemon got a message: %s", raw)
	}
	if !websocket.IsCloseError(err, 4401) {
		t.Fatalf("err = %v, want close 4401", err)
	}
}

// A session pod's connection is bound to its session, and its write pump drops every message for
// another session: the welcome carries the bound session id, so it reaches the pod.
func TestDaemonWelcomeReachesABoundRuntimeConnection(t *testing.T) {
	_, hub, host, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-000000000a01"
	if err := db.InsertClusterSession(ctx, pool, sid, host.ID, "starting", "/repos/fixture", "fixture", "", ""); err != nil {
		t.Fatal(err)
	}
	runtime, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hub.ServeDaemon(spawnTokenDaemonTok, pool))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "runner-" + sid, Mode: "runner",
		Token: runtime, ProtocolVersion: "1", ActiveSessions: []string{}, Welcome: true}); err != nil {
		t.Fatal(err)
	}
	raw := readFirstMessage(t, conn)
	var w protocol.DaemonWelcome
	if err := json.Unmarshal(raw, &w); err != nil || w.Type != "daemon_welcome" {
		t.Fatalf("first message = %s, want daemon_welcome", raw)
	}
	if w.SessionID != sid || w.Name != "runner-"+sid || w.DaemonID == "" {
		t.Errorf("pod welcome = %+v, want session %s", w, sid)
	}
	if strings.Contains(string(raw), runtime) {
		t.Fatal("the welcome echoes the runtime token")
	}
}

// The welcome goes out before the reconnect's slower work (reconcile, kills, token heal,
// replays): a daemon is told it was accepted before anything else reaches it.
func TestDaemonWelcomeComesBeforeTheReconnectWork(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	ownedToken := "bdt_" + strings.Repeat("a", 64)
	desktopFakeCore(t, api, map[string]desktopauth.Binding{ownedToken: *dc.DesktopBinding})
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	now := time.Now()
	if err := db.EndSessionStatus(ctx, pool, sessionID, "ended", &now, systemEnd(db.EndReasonStoppedByUser)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hub.ServeDaemon(spawnTokenDaemonTok, pool))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The same daemon comes back still hosting the session that was stopped while it was away.
	if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: dc.Name, Mode: "local", ReposRoot: "/repos",
		Token: ownedToken, DesktopID: dc.ID, ProtocolVersion: "1", ActiveSessions: []string{sessionID}, Welcome: true}); err != nil {
		t.Fatal(err)
	}
	var types []string
	for len(types) < 2 {
		var env envelope
		raw := readFirstMessage(t, conn)
		_ = json.Unmarshal(raw, &env)
		types = append(types, env.Type)
	}
	if types[0] != "daemon_welcome" || types[1] != "kill_session" {
		t.Fatalf("messages = %v, want daemon_welcome first, then the kill", types)
	}
}

// The daemon and the server together: the real client against the real hello handler records
// "connected" for the right token and "refused" for a wrong one, in the file the installer reads.
func TestDaemonClientAgainstServeDaemon(t *testing.T) {
	for _, tc := range []struct {
		token, want string
	}{
		{"tok-1234567890", daemon.ConnStateConnected},
		{"wrong-token-000000", daemon.ConnStateRefused},
	} {
		t.Run(tc.want, func(t *testing.T) {
			srv := httptest.NewServer(NewHub().ServeDaemon("tok-1234567890", nil))
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "connection.json")
			c := daemon.NewWSClient(daemon.Config{
				ServerURL: "ws" + strings.TrimPrefix(srv.URL, "http"), DaemonToken: tc.token, DaemonName: "box",
				DaemonMode: "local", ReposRoot: t.TempDir(), HeartbeatInterval: time.Hour,
				ReconnectInitial: 20 * time.Millisecond, ReconnectMax: 40 * time.Millisecond,
				ConnectionFile: path, UnconfirmedAfter: time.Minute, // only a welcome can say connected here
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); c.Run(ctx, func() []string { return nil }) }()
			defer func() { cancel(); <-done }()
			deadline := time.Now().Add(15 * time.Second)
			for {
				var st daemon.ConnectionState
				raw, _ := os.ReadFile(path)
				if json.Unmarshal(raw, &st) == nil && st.State == tc.want {
					if strings.Contains(string(raw), tc.token) {
						t.Fatal("the token reached the connection file")
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("connection file = %s, want state %s", raw, tc.want)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestDaemonHelloUsageNoticeCapability(t *testing.T) {
	for _, capable := range []bool{false, true} {
		name := "old"
		if capable {
			name = "capable"
		}
		t.Run(name, func(t *testing.T) {
			hub := NewHub()
			conn := dialDaemon(t, hub)
			if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "usage-box", Token: "tok-1234567890", ProtocolVersion: "1", Welcome: true, UsageNotices: capable}); err != nil {
				t.Fatal(err)
			}
			readFirstMessage(t, conn)
			// The server sends the welcome before it registers the daemon, so wait for registration.
			waitFor(t, "the daemon to register", func() bool { return len(hub.GetAllDaemons()) == 1 })
			daemons := hub.GetAllDaemons()
			if daemons[0].canUsageNotices() != capable {
				t.Fatalf("usage_notices hello capability was not retained: capable=%v", capable)
			}
		})
	}
}
