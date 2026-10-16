package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// fakeDaemonToken is obviously not a real credential; the tests check it never reaches the
// connection file.
const fakeDaemonToken = "fake-daemon-token-for-tests-0000"

// syncBuffer is a log sink the client's goroutines may write to while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLinkLog(t *testing.T) *syncBuffer {
	t.Helper()
	b := &syncBuffer{}
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return b
}

// readConnFile reads connection.json; ok is false while there is none.
func readConnFile(t *testing.T, path string) (st ConnectionState, raw string, ok bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ConnectionState{}, "", false
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("connection file is not JSON (a torn write?): %q", b)
	}
	return st, string(b), true
}

// waitConnState waits until connection.json says state.
func waitConnState(t *testing.T, path, state string) ConnectionState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, _, ok := readConnFile(t, path); ok && st.State == state {
			return st
		}
		if time.Now().After(deadline) {
			st, raw, _ := readConnFile(t, path)
			t.Fatalf("connection state never became %q (now %q: %s)", state, st.State, raw)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// helloThen is a test server that reads the hello, hands it to the test and then runs after.
func helloThen(t *testing.T, hellos chan<- protocol.DaemonHello, after func(conn *websocket.Conn)) *testServer {
	return newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var h protocol.DaemonHello
		_ = json.Unmarshal(raw, &h)
		if hellos != nil {
			select {
			case hellos <- h:
			default:
			}
		}
		after(conn)
	})
}

func sendWelcome(conn *websocket.Conn, name string) {
	_ = conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome", DaemonID: "0d0d0d0d-0000-4000-8000-000000000001", Name: name})
}

// drain reads until the connection closes, so the server holds it open.
func drain(conn *websocket.Conn) {
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func connTestClient(t *testing.T, url, mode string) (*WSClient, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "connection.json")
	c := NewWSClient(Config{
		ServerURL: url, DaemonToken: fakeDaemonToken, DaemonName: "test-box", DaemonMode: mode,
		ReposRoot: t.TempDir(), HeartbeatInterval: time.Hour,
		ReconnectInitial: 10 * time.Millisecond, ReconnectMax: 40 * time.Millisecond,
		ConnectionFile: path, UnconfirmedAfter: 150 * time.Millisecond,
	})
	return c, path
}

func runClient(t *testing.T, c *WSClient) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx, func() []string { return nil }) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

// The hello says the daemon understands the welcome, and "connected" is logged and recorded only
// once the server has sent it: the welcome, not the written hello, is what proves the token was
// accepted.
func TestWelcomeConfirmsTheConnection(t *testing.T) {
	logs := captureLinkLog(t)
	hellos := make(chan protocol.DaemonHello, 1)
	release := make(chan struct{})
	ts := helloThen(t, hellos, func(conn *websocket.Conn) {
		<-release
		sendWelcome(conn, "test-box")
		drain(conn)
	})
	c, path := connTestClient(t, ts.wsURL(), "local")
	runClient(t, c)
	h := <-hellos
	if !h.Welcome {
		t.Fatal("the hello does not say the daemon understands daemon_welcome")
	}
	time.Sleep(50 * time.Millisecond) // well inside the unconfirmed window
	if strings.Contains(logs.String(), "daemon: connected to") {
		t.Fatalf("logged connected before the server confirmed it:\n%s", logs.String())
	}
	if st, _, ok := readConnFile(t, path); ok && st.State != ConnStateConnecting {
		t.Fatalf("state before the welcome = %q, want connecting", st.State)
	}
	close(release)
	st := waitConnState(t, path, ConnStateConnected)
	if st.PID != os.Getpid() || st.Since == "" || st.Server != ts.wsURL() {
		t.Errorf("connected state = %+v", st)
	}
	if _, err := time.Parse(time.RFC3339, st.Since); err != nil {
		t.Errorf("since %q is not RFC3339", st.Since)
	}
	if want := `daemon: connected to ` + ts.wsURL() + ` as "test-box"`; !strings.Contains(logs.String(), want) {
		t.Errorf("log lacks %q:\n%s", want, logs.String())
	}
	_, raw, _ := readConnFile(t, path)
	if strings.Contains(raw, fakeDaemonToken) || strings.Contains(logs.String(), fakeDaemonToken) {
		t.Fatal("the token reached the connection file or the log")
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("connection file mode = %v (%v), want 0600", fi.Mode().Perm(), err)
	}
	// Atomic writes leave nothing else behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("state dir holds %v, want only connection.json", names)
	}
}

// A 0.2.0 server never sends a welcome. A link that stays open for the window is taken as
// accepted, and both the log and the file say the server did not confirm it.
func TestNoWelcomeIsUnconfirmedAfterTheWindow(t *testing.T) {
	logs := captureLinkLog(t)
	ts := helloThen(t, nil, drain)
	c, path := connTestClient(t, ts.wsURL(), "local")
	runClient(t, c)
	st := waitConnState(t, path, ConnStateUnconfirmed)
	if st.PID != os.Getpid() || st.Detail == "" {
		t.Errorf("unconfirmed state = %+v", st)
	}
	out := logs.String()
	if !strings.Contains(out, "not confirmed") {
		t.Errorf("log does not say the connection was not confirmed:\n%s", out)
	}
	if strings.Contains(out, `as "test-box"`) || strings.Contains(out, "daemon: connected to") {
		t.Errorf("logged a confirmed connection that the server never confirmed:\n%s", out)
	}
}

// A welcome that arrives after the window (a slow attach on a 0.2.1 server) still moves the state
// to connected and logs the normal line.
func TestLateWelcomeMovesUnconfirmedToConnected(t *testing.T) {
	logs := captureLinkLog(t)
	release := make(chan struct{})
	ts := helloThen(t, nil, func(conn *websocket.Conn) {
		<-release
		sendWelcome(conn, "test-box")
		drain(conn)
	})
	c, path := connTestClient(t, ts.wsURL(), "local")
	runClient(t, c)
	waitConnState(t, path, ConnStateUnconfirmed)
	close(release)
	waitConnState(t, path, ConnStateConnected)
	if !strings.Contains(logs.String(), `daemon: connected to `+ts.wsURL()+` as "test-box"`) {
		t.Errorf("the late welcome was not logged:\n%s", logs.String())
	}
}

// refusingServer closes every connection with 4401 right after the hello, as the server does for a
// wrong token.
func refusingServer(t *testing.T) *testServer {
	return helloThen(t, nil, func(conn *websocket.Conn) {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4401, "unauthorized"))
	})
}

// recordSleeps replaces the backoff wait and stops the client after n of them.
func recordSleeps(c *WSClient, n int, cancel context.CancelFunc) *[]time.Duration {
	var mu sync.Mutex
	sleeps := &[]time.Duration{}
	c.sleep = func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		*sleeps = append(*sleeps, d)
		full := len(*sleeps) >= n
		mu.Unlock()
		if full {
			cancel()
			return false
		}
		time.Sleep(time.Millisecond)
		return ctx.Err() == nil
	}
	return sleeps
}

// A 4401 is said loudly, once per refusal (not as a generic disconnect), names where the desktop
// daemon's token comes from, is recorded as refused, and never resets the backoff: a refused
// daemon must not retry every 2 s forever.
func TestRefusedTokenIsLoudRecordedAndBacksOff(t *testing.T) {
	logs := captureLinkLog(t)
	ts := refusingServer(t)
	c, path := connTestClient(t, ts.wsURL(), "local")
	ctx, cancel := context.WithCancel(context.Background())
	sleeps := recordSleeps(c, 4, cancel)
	c.Run(ctx, func() []string { return nil })
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond}
	if !slices.Equal(*sleeps, want) {
		t.Fatalf("sleeps after 4401 = %v, want %v (a refusal must not reset the ladder)", *sleeps, want)
	}
	st, raw, ok := readConnFile(t, path)
	if !ok || st.State != ConnStateRefused || st.PID != os.Getpid() {
		t.Fatalf("state after 4401 = %s", raw)
	}
	out := logs.String()
	if !strings.Contains(out, "refused the daemon token") || !strings.Contains(out, "install/desktop/.env") ||
		!strings.Contains(out, "BLERG_RUNNER_DAEMON_TOKEN") {
		t.Errorf("the 4401 line does not say what was refused and where the token comes from:\n%s", out)
	}
	if n := strings.Count(out, "refused the daemon token"); n != 1 {
		t.Errorf("the refusal was logged %d times in a burst, want once:\n%s", n, out)
	}
	if strings.Contains(out, "daemon: connected to") {
		t.Errorf("a refused daemon logged connected:\n%s", out)
	}
	if strings.Contains(out, fakeDaemonToken) || strings.Contains(raw, fakeDaemonToken) {
		t.Fatal("the token reached the log or the connection file")
	}
}

// A session pod is told its runtime token was refused (there is no .env in a pod), and it writes
// no connection file: its HOME is the agent's workspace.
func TestRefusedRuntimeTokenInARunnerPod(t *testing.T) {
	logs := captureLinkLog(t)
	ts := refusingServer(t)
	c, path := connTestClient(t, ts.wsURL(), "runner")
	ctx, cancel := context.WithCancel(context.Background())
	recordSleeps(c, 2, cancel)
	c.Run(ctx, func() []string { return nil })
	out := logs.String()
	if !strings.Contains(out, "runtime token") || strings.Contains(out, ".env") {
		t.Errorf("a pod's 4401 line must speak of its runtime token, not .env:\n%s", out)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("a runner-mode client wrote into the state dir (%v)", err)
	}
}

// For the backoff, a link counts as up once it was confirmed (welcome) or stayed open for the
// unconfirmed window; a close before either is a failed attempt and the ladder keeps climbing.
func TestBackoffResetsOnlyForALinkThatWasUp(t *testing.T) {
	captureLinkLog(t)
	cases := []struct {
		name  string
		after func(conn *websocket.Conn)
		want  []time.Duration
	}{
		{"welcome then close", func(conn *websocket.Conn) {
			sendWelcome(conn, "test-box")
			time.Sleep(20 * time.Millisecond)
		}, []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}},
		{"open past the window, no welcome", func(_ *websocket.Conn) {
			time.Sleep(250 * time.Millisecond)
		}, []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}},
		{"closed at once, no welcome", func(_ *websocket.Conn) {},
			[]time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := helloThen(t, nil, tc.after)
			c, _ := connTestClient(t, ts.wsURL(), "local")
			ctx, cancel := context.WithCancel(context.Background())
			sleeps := recordSleeps(c, 3, cancel)
			c.Run(ctx, func() []string { return nil })
			if !slices.Equal(*sleeps, tc.want) {
				t.Errorf("sleeps = %v, want %v", *sleeps, tc.want)
			}
		})
	}
}

// Before its first dial the daemon replaces whatever an earlier process left with "connecting"
// and its own pid, so a stale "connected" never outlives the process that wrote it.
func TestConnectingIsWrittenBeforeTheFirstDial(t *testing.T) {
	captureLinkLog(t)
	c, path := connTestClient(t, "ws://127.0.0.1:1/ws/daemon", "local") // nothing listens
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := `{"state":"connected","server":"ws://old","since":"2020-01-01T00:00:00Z","detail":"","pid":1}`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var first ConnectionState
	c.sleep = func(_ context.Context, _ time.Duration) bool {
		first, _, _ = readConnFile(t, path)
		cancel()
		return false
	}
	c.Run(ctx, func() []string { return nil })
	if first.State != ConnStateConnecting || first.PID != os.Getpid() || first.Server != "ws://127.0.0.1:1/ws/daemon" {
		t.Fatalf("state at the first retry = %+v, want connecting with this pid", first)
	}
	if first.Detail == "" {
		t.Error("connecting after a failed dial says nothing about why")
	}
}

// The file names the server without anything that could be a credential in its URL.
func TestConnectionFileStripsURLUserinfo(t *testing.T) {
	if got := redactServerURL("wss://user:secret@runner.example.com/ws/daemon?token=x"); got != "wss://runner.example.com/ws/daemon" {
		t.Errorf("redactServerURL = %q", got)
	}
}
