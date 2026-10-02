package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

var testUpgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// testServer is a minimal WebSocket server for testing. Each incoming
// connection is handled by calling connHandler in its own goroutine.
type testServer struct {
	server      *httptest.Server
	connHandler func(conn *websocket.Conn)
}

func newTestServer(t *testing.T, handler func(conn *websocket.Conn)) *testServer {
	t.Helper()
	ts := &testServer{connHandler: handler}
	ts.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("test server upgrade error: %v", err)
			return
		}
		ts.connHandler(conn)
	}))
	t.Cleanup(ts.server.Close)
	return ts
}

// wsURL returns the WebSocket URL for the test server.
func (ts *testServer) wsURL() string {
	return "ws" + ts.server.URL[len("http"):]
}

func TestWSClient(t *testing.T) {
	// helloCh receives every daemon_hello the test server sees.
	helloCh := make(chan protocol.DaemonHello, 4)

	// connCh receives a signal for each new WS connection accepted (so we can
	// close the first one after asserting hello).
	connReadyCh := make(chan *websocket.Conn, 4)

	ts := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()

		// Signal that a new connection arrived.
		connReadyCh <- conn

		// Read messages until the connection closes, forwarding any
		// daemon_hello messages to helloCh.
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				continue
			}
			if env.Type == "daemon_hello" {
				var hello protocol.DaemonHello
				if err := json.Unmarshal(raw, &hello); err == nil {
					helloCh <- hello
				}
			}
		}
	})

	cfg := Config{
		ServerURL:         ts.wsURL(),
		DaemonToken:       "test-token",
		DaemonName:        "test-daemon",
		DaemonMode:        "local",
		ReposRoot:         "/tmp/repos",
		ProtocolVersion:   "1",
		HeartbeatInterval: 5 * time.Minute, // don't fire during test
		ReconnectInitial:  100 * time.Millisecond,
		ReconnectMax:      200 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := NewWSClient(cfg)
	go client.Run(ctx, func() []string { return []string{} })

	// ── Assert: first daemon_hello received ───────────────────────────────────
	var firstHello protocol.DaemonHello
	select {
	case firstHello = <-helloCh:
	case <-ctx.Done():
		t.Fatal("timed out waiting for first daemon_hello")
	}

	if firstHello.Type != "daemon_hello" {
		t.Errorf("first message type = %q, want daemon_hello", firstHello.Type)
	}
	if firstHello.Name != "test-daemon" {
		t.Errorf("name = %q, want test-daemon", firstHello.Name)
	}
	if firstHello.Token != "test-token" {
		t.Errorf("token = %q, want test-token", firstHello.Token)
	}
	if firstHello.Mode != "local" {
		t.Errorf("mode = %q, want local", firstHello.Mode)
	}
	if firstHello.ReposRoot != "/tmp/repos" {
		t.Errorf("repos_root = %q, want /tmp/repos", firstHello.ReposRoot)
	}
	if firstHello.ProtocolVersion != "1" {
		t.Errorf("protocol_version = %q, want 1", firstHello.ProtocolVersion)
	}
	if !firstHello.MCPGateway {
		t.Error("hello does not report the mcp_gateway capability")
	}
	if !firstHello.RestrictTools {
		t.Error("hello does not report the restrict_tools capability")
	}

	// ── Drop the first connection ─────────────────────────────────────────────
	firstConn := <-connReadyCh
	firstConn.Close()

	// ── Assert: client reconnects and sends daemon_hello again ────────────────
	var secondHello protocol.DaemonHello
	select {
	case secondHello = <-helloCh:
	case <-ctx.Done():
		t.Fatal("timed out waiting for second daemon_hello after reconnect")
	}

	if secondHello.Type != "daemon_hello" {
		t.Errorf("second message type = %q, want daemon_hello", secondHello.Type)
	}
	if secondHello.Name != "test-daemon" {
		t.Errorf("second hello name = %q, want test-daemon", secondHello.Name)
	}
}

// TestListReposOnDiskExcludesDotdirs verifies that hidden directories (like
// .blerg-runner's own state dir) never show up as bogus repo entries in the
// launch UI's picker.
func TestListReposOnDiskExcludesDotdirs(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"blerg", "clarity", ".blerg-runner", ".git", "example"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	// A plain file (not a directory) should also never appear.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got := listReposOnDisk(root)
	sort.Strings(got)
	want := []string{"blerg", "clarity", "example"}
	if !slices.Equal(got, want) {
		t.Errorf("listReposOnDisk(%s) = %v, want %v", root, got, want)
	}
}

// TestHelloAndHeartbeatReportRepoRemotes verifies a daemon reports each
// checked-out folder's GitHub identity (from a real `git remote add`), keyed
// by folder name — including a folder whose name differs from its remote's
// repository name — and omits folders with no GitHub origin.
func TestHelloAndHeartbeatReportRepoRemotes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	mkRepo := func(name, remote string) {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		args := [][]string{{"init", "-q"}}
		if remote != "" {
			args = append(args, []string{"remote", "add", "origin", remote})
		}
		for _, a := range args {
			cmd := exec.Command("git", a...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", a, err, out)
			}
		}
	}
	mkRepo("entertainment", "git@github.com:example-org/example-repo.git")
	mkRepo("widget", "https://github.com/example-org/widget")
	mkRepo("elsewhere", "https://git.example.test/example-org/elsewhere.git")
	mkRepo("on-gitlab", "git@gitlab.com:example-group/on-gitlab.git")
	mkRepo("scratch", "")
	if err := os.Mkdir(filepath.Join(root, "plain-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	type msg struct {
		Type        string                         `json:"type"`
		RepoRemotes map[string]string              `json:"repo_remotes"`
		RepoOrigins map[string]protocol.RepoOrigin `json:"repo_origins"`
	}
	// repo_origins carries every provider; the legacy repo_remotes stays
	// GitHub-only so an older server never reads a GitLab repo as GitHub.
	wantOrigins := map[string]protocol.RepoOrigin{
		"entertainment": {Provider: "github", FullName: "example-org/example-repo"},
		"widget":        {Provider: "github", FullName: "example-org/widget"},
		"on-gitlab":     {Provider: "gitlab", FullName: "example-group/on-gitlab"},
	}
	msgs := make(chan msg, 8)
	ts := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m msg
			if json.Unmarshal(raw, &m) == nil {
				msgs <- m
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := NewWSClient(Config{
		ServerURL: ts.wsURL(), DaemonToken: "t", DaemonName: "d", DaemonMode: "local",
		ReposRoot: root, ProtocolVersion: "1",
		HeartbeatInterval: 50 * time.Millisecond,
		ReconnectInitial:  100 * time.Millisecond, ReconnectMax: 200 * time.Millisecond,
	})
	go client.Run(ctx, func() []string { return nil })

	want := map[string]string{"entertainment": "example-org/example-repo", "widget": "example-org/widget"}
	seen := map[string]bool{}
	for !seen["daemon_hello"] || !seen["daemon_heartbeat"] {
		select {
		case m := <-msgs:
			if m.Type != "daemon_hello" && m.Type != "daemon_heartbeat" {
				continue
			}
			seen[m.Type] = true
			if len(m.RepoRemotes) != len(want) {
				t.Fatalf("%s repo_remotes = %v, want %v", m.Type, m.RepoRemotes, want)
			}
			for k, v := range want {
				if m.RepoRemotes[k] != v {
					t.Errorf("%s repo_remotes[%q] = %q, want %q", m.Type, k, m.RepoRemotes[k], v)
				}
			}
			if len(m.RepoOrigins) != len(wantOrigins) {
				t.Fatalf("%s repo_origins = %v, want %v", m.Type, m.RepoOrigins, wantOrigins)
			}
			for k, v := range wantOrigins {
				if m.RepoOrigins[k] != v {
					t.Errorf("%s repo_origins[%q] = %+v, want %+v", m.Type, k, m.RepoOrigins[k], v)
				}
			}
		case <-ctx.Done():
			t.Fatalf("timed out; seen %v", seen)
		}
	}
}

// TestReconnectBackoffGrows verifies that Run's reconnect delay grows
// exponentially (doubling each failed attempt) and caps at ReconnectMax,
// rather than resetting to the floor on every loop iteration regardless of
// outcome. With nothing listening on the dial address, every connect attempt
// fails before a successful connection ever occurs, so the ladder must keep
// climbing (and stay capped) across all four recorded sleeps.
func TestReconnectBackoffGrows(t *testing.T) {
	c := NewWSClient(Config{
		ServerURL:        "ws://127.0.0.1:1/ws/daemon", // nothing listens: every connect fails
		DaemonToken:      "t",
		DaemonName:       "d",
		DaemonMode:       "local",
		ReposRoot:        t.TempDir(),
		ReconnectInitial: 2 * time.Millisecond,
		ReconnectMax:     8 * time.Millisecond,
	})
	var sleeps []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(_ context.Context, d time.Duration) bool {
		sleeps = append(sleeps, d)
		if len(sleeps) == 4 {
			cancel()
			return false
		}
		return true
	}
	c.Run(ctx, func() []string { return nil })
	want := []time.Duration{2 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond, 8 * time.Millisecond}
	if !slices.Equal(sleeps, want) {
		t.Fatalf("sleeps = %v, want %v (backoff must grow, not reset every attempt)", sleeps, want)
	}
}
