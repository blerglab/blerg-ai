package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// The hello limit is sized from a measured worst-case legitimate hello.
func TestDaemonReadLimitFitsWorstCaseHello(t *testing.T) {
	long := func(prefix string, i, n int) string {
		s := fmt.Sprintf("%s-%d-", prefix, i)
		return s + strings.Repeat("x", n-len(s))
	}
	hello := protocol.DaemonHello{Type: "daemon_hello", Name: strings.Repeat("h", 64), ReposRoot: "/" + strings.Repeat("r", 250),
		Token: strings.Repeat("t", 128), ProtocolVersion: "1", Version: "v1.2.3", SandboxAvailable: true,
		AvailableEngines: []string{"claude", "codex", "hermes", "openclaw"}, RepoRemotes: map[string]string{}}
	for i := range 500 {
		hello.ActiveSessions = append(hello.ActiveSessions, fmt.Sprintf("%08d-0000-4000-8000-000000000000", i))
	}
	for i := range 2000 {
		hello.CheckedOutRepos = append(hello.CheckedOutRepos, long("repo", i, 100))
	}
	for i := range 1000 {
		hello.RepoRemotes[hello.CheckedOutRepos[i]] = long("org", i, 39) + "/" + long("repo", i, 100)
	}
	// Every engine that could report, at the per-engine cap, every field at
	// its maximum (names/descriptions in 3-byte runes).
	hello.EngineModels = map[string]models.Report{}
	for _, e := range models.Engines() {
		var ms []models.Model
		for i := range models.MaxReportedModels {
			ms = append(ms, models.Model{ID: long("m", i, 128), Name: strings.Repeat("名", 64),
				Description: strings.Repeat("説", 200), Section: "overflow",
				Efforts: models.EffortsFor(e), DefaultEffort: "medium", EffortKind: "reasoning"})
		}
		hello.EngineModels[e] = models.Report{Models: ms, FetchedAt: time.Now()}
	}
	raw, _ := json.Marshal(hello)
	t.Logf("worst-case hello: %d bytes (limit %d)", len(raw), daemonHelloReadLimit)
	if int64(len(raw))*2 > daemonHelloReadLimit {
		t.Errorf("worst-case hello %d bytes is not well under the %d limit", len(raw), daemonHelloReadLimit)
	}
}

func dialDaemon(t *testing.T, hub *Hub) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(hub.ServeDaemon("tok-1234567890", nil))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// waitClosed reads until the server closes the socket.
func waitClosed(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("the server did not close the connection")
			}
			return
		}
	}
}

func TestDaemonOversizedHelloClosesTheConnection(t *testing.T) {
	prev := daemonHelloReadLimit
	daemonHelloReadLimit = 4 << 10
	t.Cleanup(func() { daemonHelloReadLimit = prev })
	hub := NewHub()
	conn := dialDaemon(t, hub)
	hello := protocol.DaemonHello{Type: "daemon_hello", Name: "box", Token: "tok-1234567890", ProtocolVersion: "1",
		CheckedOutRepos: []string{strings.Repeat("x", 8<<10)}}
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, conn)
	if n := len(hub.GetAllDaemons()); n != 0 {
		t.Errorf("an oversized hello registered a daemon (%d)", n)
	}
}

func TestDaemonOversizedMessageClosesTheConnection(t *testing.T) {
	prev := daemonMessageReadLimit
	daemonMessageReadLimit = 8 << 10
	t.Cleanup(func() { daemonMessageReadLimit = prev })
	hub := NewHub()
	conn := dialDaemon(t, hub)
	// A normal hello and heartbeat pass.
	if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "box", Token: "tok-1234567890", ProtocolVersion: "1",
		EngineModels: map[string]models.Report{"codex": {Models: []models.Model{{ID: "gpt-a", Efforts: []string{}}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ActiveSessions: []string{}, CheckedOutRepos: []string{"app"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if ds := hub.GetAllDaemons(); len(ds) == 1 && len(ds[0].CheckedOutRepos()) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the normal hello/heartbeat did not register")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A frame over the limit ends the connection (and unregisters the daemon).
	if err := conn.WriteJSON(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", CheckedOutRepos: []string{strings.Repeat("x", 16<<10)}}); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, conn)
	deadline = time.Now().Add(3 * time.Second)
	for len(hub.GetAllDaemons()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the daemon stayed registered after an oversized frame")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
