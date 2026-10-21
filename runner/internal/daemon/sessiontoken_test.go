package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// The server's session_token message, exactly as it goes on the wire
// (protocol.SessionToken), reaches the handler NewManager registers and lands
// in the token file the CLI reads: private, replaced whole on a second one.
func TestSessionTokenMessageWritesTheTokenFile(t *testing.T) {
	state := t.TempDir()
	t.Setenv(DaemonStateDirEnv, state)
	noSandboxes(t)
	client := NewWSClient(Config{})
	NewManager(client, ManagerConfig{ReposRoot: t.TempDir(), GithubOrg: "org", Command: []string{"unused"}})
	handle := client.handlers["session_token"]
	if handle == nil {
		t.Fatal("no session_token handler registered")
	}

	const id = "0f0e0d0c-0b0a-4908-8706-050403020100"
	handle([]byte(`{"type":"session_token","session_id":"` + id + `","token":"aaaa1111"}`))
	path := filepath.Join(state, "session-tokens", id)
	if got := SessionTokenPath(state, id); got != path {
		t.Fatalf("SessionTokenPath = %q, want %q", got, path)
	}
	assertTokenFile(t, path, "aaaa1111")
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("token dir: %v, %v (want 0700)", info, err)
	}

	handle([]byte(`{"type":"session_token","session_id":"` + id + `","token":"bbbb2222"}`))
	assertTokenFile(t, path, "bbbb2222")
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("left %d files behind, want only the token file", len(entries))
	}

	removeSessionTokenFile(state, id)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("token file not removed: %v", err)
	}
}

// A message whose id is not a plain file name, or whose token could forge a
// second line, writes nothing anywhere.
func TestSessionTokenMessageRefusesMalformedInput(t *testing.T) {
	state := t.TempDir()
	t.Setenv(DaemonStateDirEnv, state)
	noSandboxes(t)
	m := &Manager{}
	for _, raw := range []string{
		`{"type":"session_token","session_id":"../escape","token":"aaaa"}`,
		`{"type":"session_token","session_id":"","token":"aaaa"}`,
		`{"type":"session_token","session_id":"abc","token":""}`,
		`{"type":"session_token","session_id":"abc","token":"aa\nBLERG=1"}`,
		`not json`,
	} {
		m.handleSessionToken([]byte(raw))
	}
	var files []string
	_ = filepath.Walk(filepath.Dir(state), func(p string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Fatalf("malformed session_token wrote %v", files)
	}
}

// A sandboxed session's CLI runs in its container, which has no view of the
// host's state directory: the token is written in there too, on stdin.
func TestSessionTokenReachesASandboxedSession(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	const id = "11111111-2222-4333-8444-555555555555"
	container := dockerContainerName(id)
	origOut := dockerOutput
	dockerOutput = func(args ...string) ([]byte, error) { return []byte(container + "\n"), nil }
	t.Cleanup(func() { dockerOutput = origOut })
	var got []string
	origWrite := writeSandboxTokenFile
	writeSandboxTokenFile = func(c, sid, tok string) error { got = []string{c, sid, tok}; return nil }
	t.Cleanup(func() { writeSandboxTokenFile = origWrite })

	(&Manager{}).handleSessionToken([]byte(`{"type":"session_token","session_id":"` + id + `","token":"cccc3333"}`))
	if !slices.Equal(got, []string{container, id, "cccc3333"}) {
		t.Fatalf("sandbox write = %v", got)
	}
	// The in-container script reads the token from stdin and writes the path
	// the CLI derives from its own HOME.
	if strings.Contains(sandboxTokenScript, "cccc") || !strings.Contains(sandboxTokenScript, `$HOME/.blerg-runner-daemon/session-tokens`) {
		t.Fatalf("sandbox script = %q", sandboxTokenScript)
	}
}

// The daemon's state directory is a host path: it is not forwarded into a
// sandbox, where the CLI must look under the container's own HOME.
func TestSandboxEnvDropsTheDaemonStateDir(t *testing.T) {
	args := sandboxEnvArgs([]string{DaemonStateDirEnv + "=/home/someone/.blerg-runner-daemon", "BLERG_RUNNER_SESSION_ID=x"})
	if strings.Contains(strings.Join(args, " "), DaemonStateDirEnv) || !slices.Contains(args, "BLERG_RUNNER_SESSION_ID=x") {
		t.Fatalf("sandbox env args = %v", args)
	}
}

func noSandboxes(t *testing.T) {
	t.Helper()
	orig := dockerOutput
	dockerOutput = func(args ...string) ([]byte, error) { return nil, nil }
	t.Cleanup(func() { dockerOutput = orig })
}

func assertTokenFile(t *testing.T, path, want string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != want {
		t.Fatalf("token file content wrong (err %v)", err)
	}
}

// A spawn the daemon has received but not started is reported as queued —
// from the moment it is queued, through its clone — and not once it is
// killed; so the server never reads a slow start as a lost session.
func TestQueuedSpawnIDsListsAWaitingSpawn(t *testing.T) {
	root := t.TempDir()
	client := NewWSClient(Config{})
	mgr := NewManager(client, ManagerConfig{ReposRoot: root, GithubOrg: "org", Command: []string{"unused"}})
	entered := make(chan struct{})
	release := make(chan struct{})
	mgr.ensureClonedFunc = func(_ context.Context, reposRoot, repo, provider, org string) error {
		close(entered)
		<-release
		return errors.New("test: no clone")
	}
	raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-queued", Repo: "cold", Kind: "agent"})
	client.handlers["spawn_session"](raw)
	if got := mgr.QueuedSpawnIDs(); !slices.Equal(got, []string{"s-queued"}) {
		t.Fatalf("queued right after enqueue = %v", got)
	}
	<-entered
	if got := mgr.QueuedSpawnIDs(); !slices.Equal(got, []string{"s-queued"}) {
		t.Fatalf("queued while cloning = %v", got)
	}
	if slices.Contains(mgr.ActiveSessionIDs(), "s-queued") {
		t.Fatal("a queued spawn is reported as a running session")
	}
	killRaw, _ := json.Marshal(protocol.KillSession{Type: "kill_session", SessionID: "s-queued"})
	client.handlers["kill_session"](killRaw)
	if got := mgr.QueuedSpawnIDs(); len(got) != 0 {
		t.Fatalf("a killed spawn is still reported queued: %v", got)
	}
	close(release)
}

// The hello and every heartbeat carry the queued spawns and the hello says
// the daemon takes a re-issued session token.
func TestHelloAndHeartbeatReportQueuedSpawns(t *testing.T) {
	type msg struct {
		Type             string   `json:"type"`
		QueuedSessions   []string `json:"queued_sessions"`
		SessionTokenFile bool     `json:"session_token_file"`
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := NewWSClient(Config{
		ServerURL: ts.wsURL(), DaemonToken: "t", DaemonName: "d", DaemonMode: "local",
		ReposRoot: t.TempDir(), ProtocolVersion: "1", HeartbeatInterval: 50 * time.Millisecond,
	})
	client.SetQueuedSessions(func() []string { return []string{"s-q"} })
	go client.Run(ctx, func() []string { return nil })
	seen := map[string]bool{}
	for !seen["daemon_hello"] || !seen["daemon_heartbeat"] {
		select {
		case m := <-msgs:
			if m.Type != "daemon_hello" && m.Type != "daemon_heartbeat" {
				continue
			}
			seen[m.Type] = true
			if !slices.Equal(m.QueuedSessions, []string{"s-q"}) {
				t.Fatalf("%s queued_sessions = %v", m.Type, m.QueuedSessions)
			}
			if m.Type == "daemon_hello" && !m.SessionTokenFile {
				t.Fatal("the hello does not say the daemon takes a re-issued token")
			}
		case <-ctx.Done():
			t.Fatalf("timed out; seen %v", seen)
		}
	}
}

// tokenResults is the SessionTokenResult messages r recorded.
func tokenResults(r *recordingSender) []protocol.SessionTokenResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []protocol.SessionTokenResult
	for _, v := range r.msgs {
		if m, ok := v.(protocol.SessionTokenResult); ok {
			out = append(out, m)
		}
	}
	return out
}

// The daemon answers every session_token: ok once stored, not ok (with a
// reason, never the token) when it could not be.
func TestSessionTokenIsAnswered(t *testing.T) {
	noSandboxes(t)
	const id = "22222222-3333-4444-8555-666666666666"
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	rec := newRecordingSender(4)
	m := &Manager{client: rec}
	m.handleSessionToken([]byte(`{"type":"session_token","session_id":"` + id + `","token":"dddd4444"}`))
	if got := tokenResults(rec); len(got) != 1 || !got[0].OK || got[0].SessionID != id {
		t.Fatalf("answer to a stored token = %+v", got)
	}

	// A state directory that cannot be written: refused, and said so.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DaemonStateDirEnv, blocked)
	rec2 := newRecordingSender(4)
	(&Manager{client: rec2}).handleSessionToken([]byte(`{"type":"session_token","session_id":"` + id + `","token":"eeee5555"}`))
	got := tokenResults(rec2)
	if len(got) != 1 || got[0].OK || got[0].Error == "" || strings.Contains(got[0].Error, "eeee5555") {
		t.Fatalf("answer to a token that could not be stored = %+v", got)
	}

	// A sandboxed session whose container write fails: not ok either.
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	origOut := dockerOutput
	dockerOutput = func(args ...string) ([]byte, error) { return []byte(dockerContainerName(id) + "\n"), nil }
	t.Cleanup(func() { dockerOutput = origOut })
	origWrite := writeSandboxTokenFile
	writeSandboxTokenFile = func(c, sid, tok string) error { return errors.New("exec failed") }
	t.Cleanup(func() { writeSandboxTokenFile = origWrite })
	rec3 := newRecordingSender(4)
	(&Manager{client: rec3}).handleSessionToken([]byte(`{"type":"session_token","session_id":"` + id + `","token":"ffff6666"}`))
	if got := tokenResults(rec3); len(got) != 1 || got[0].OK {
		t.Fatalf("answer when the sandbox write failed = %+v", got)
	}
}

// A container made again for a session (a restart's recovery, a resume) has
// none of the files of the one before: the session's re-issued token is
// copied in from the host before anything runs in it.
func TestRecreatedSandboxGetsTheHostTokenFile(t *testing.T) {
	state := t.TempDir()
	t.Setenv(DaemonStateDirEnv, state)
	const id = "33333333-4444-4555-8666-777777777777"
	if err := writeSessionTokenFile(state, id, "gggg7777"); err != nil {
		t.Fatal(err)
	}
	recordDockerCalls(t, nil)
	var got []string
	origWrite := writeSandboxTokenFile
	writeSandboxTokenFile = func(c, sid, tok string) error { got = []string{c, sid, tok}; return nil }
	t.Cleanup(func() { writeSandboxTokenFile = origWrite })

	container, err := startSandboxContainer(id, sandboxTestClone(t, id), hostSessionEnv(), sandboxCredential{}, SandboxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{container, id, "gggg7777"}) {
		t.Fatalf("token copied into the new container = %v", got)
	}

	// No re-issued token: nothing to copy.
	got = nil
	if _, err := startSandboxContainer("44444444-5555-4666-8777-888888888888", sandboxTestClone(t, "44444444-5555-4666-8777-888888888888"), hostSessionEnv(), sandboxCredential{}, SandboxOptions{}); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("copied a token that does not exist: %v", got)
	}
}
