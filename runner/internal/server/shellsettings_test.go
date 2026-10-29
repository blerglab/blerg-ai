package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

type shellTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *shellTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *shellTestClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestShellLimitsFromSettings(t *testing.T) {
	l := shellLimitsFromSettings(nil)
	if l != ownershell.DefaultLimits() || l.Desktop != (ownershell.Limits{}) || l.Cluster.Detached != 5*time.Minute || l.Cluster.Idle != 30*time.Minute {
		t.Fatalf("defaults: %+v", l)
	}
	l = shellLimitsFromSettings(map[string]int64{
		db.SettingShellDesktopDetachedSeconds: 600,
		db.SettingShellDesktopIdleSeconds:     87600 * 3600,
		db.SettingShellClusterDetachedSeconds: 0,
		db.SettingShellClusterIdleSeconds:     599, // out of bounds: the default stays
	})
	want := ownershell.ShellLimits{Desktop: ownershell.Limits{Detached: 10 * time.Minute, Idle: 87600 * time.Hour}, Cluster: ownershell.Limits{Detached: 0, Idle: 30 * time.Minute}}
	if l != want {
		t.Fatalf("overrides: %+v, want %+v", l, want)
	}
}

func TestShellSettingsAdminOnlyBoundedAndAppliedToRunningShells(t *testing.T) {
	pool := runnerContractPool(t)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAPI(NewHub(), pool, "daemon-tok-1234567890", nil, "")
	a.coreAuth = newTestCoreAuthClient(t, pub, "shell-key")
	clock := &shellTestClock{now: time.Unix(1000, 0)}
	rt := &shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 8), input: make(chan []byte, 8)}
	m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	a.shells = m
	m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	t.Cleanup(func() { m.Unavailable(context.Background()) })
	mux := http.NewServeMux()
	a.RegisterShellRoutes(mux)
	token := func(caps ...string) string {
		return mintTestToken(t, key, "shell-key", identity.Claims{Kind: "human", Sub: "owner", Aud: coreAuthAudience, Sid: "login", Caps: append([]string{coreAuthBrowserCap}, caps...), ExpiresAt: time.Now().Add(time.Hour).Unix()})
	}
	call := func(method, tok, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/shells/settings", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	admin, person := token("account.manage"), token()

	// A person who is not an administrator can neither read nor change the limits.
	if r := call(http.MethodGet, person, ""); r.Code != http.StatusForbidden {
		t.Fatalf("non-admin GET: %d", r.Code)
	}
	if r := call(http.MethodPut, person, `{"desktop":{"detached_seconds":600}}`); r.Code != http.StatusForbidden {
		t.Fatalf("non-admin PUT: %d", r.Code)
	}
	if got, _ := db.GetSettings(t.Context(), pool); len(got) != 0 {
		t.Fatalf("a refused change was stored: %v", got)
	}
	r := call(http.MethodGet, admin, "")
	var reply shellSettingsReply
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &reply) != nil {
		t.Fatalf("admin GET: %d %s", r.Code, r.Body)
	}
	if reply.Desktop != (shellLimitPair{}) || reply.Cluster != (shellLimitPair{300, 1800}) || reply.MinSeconds != 600 || reply.MaxSeconds != 87600*3600 {
		t.Fatalf("defaults reply: %+v", reply)
	}

	// Bounds: below 10 minutes, above 87,600 hours, negative and unknown fields are refused.
	for _, body := range []string{
		`{"desktop":{"detached_seconds":599}}`,
		`{"cluster":{"idle_seconds":315360001}}`,
		`{"cluster":{"idle_seconds":-1}}`,
		`{"desktop":{"detached_seconds":600,"idle_seconds":60}}`,
	} {
		if r := call(http.MethodPut, admin, body); r.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d, want 422", body, r.Code)
		}
	}
	for _, body := range []string{`{}`, `{"desktop":{}}`, `{"laptop":{"idle_seconds":600}}`, `not json`} {
		if r := call(http.MethodPut, admin, body); r.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", body, r.Code)
		}
	}
	if got, _ := db.GetSettings(t.Context(), pool); len(got) != 0 {
		t.Fatalf("a refused change was stored: %v", got)
	}

	// A running desktop shell, detached for two hours under the default (until the session ends).
	owner := ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}
	info, err := m.Open(t.Context(), owner, "session")
	if err != nil {
		t.Fatal(err)
	}
	tick := func(d time.Duration) {
		for end := clock.Now().Add(d); clock.Now().Before(end); {
			clock.Add(10 * time.Second)
			m.Tick(t.Context())
		}
	}
	tick(2 * time.Hour)
	alive := func() bool {
		list, err := m.List(t.Context(), owner, "session")
		return err == nil && len(list) == 1
	}
	if !alive() {
		t.Fatal("a detached desktop shell ended under the default")
	}
	r = call(http.MethodPut, admin, `{"desktop":{"detached_seconds":600,"idle_seconds":0},"cluster":{"idle_seconds":7200}}`)
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &reply) != nil {
		t.Fatalf("admin PUT: %d %s", r.Code, r.Body)
	}
	if reply.Desktop != (shellLimitPair{600, 0}) || reply.Cluster != (shellLimitPair{300, 7200}) {
		t.Fatalf("reply after PUT: %+v", reply)
	}
	if got := m.Limits(); got.Desktop.Detached != 10*time.Minute || got.Cluster.Idle != 2*time.Hour {
		t.Fatalf("the manager did not get the new limits: %+v", got)
	}
	// The shortened limit gives the running shell 10 minutes from the change, then ends it.
	tick(10*time.Minute - 10*time.Second)
	if !alive() {
		t.Fatal("the running shell ended before the 10-minute floor")
	}
	tick(10 * time.Second)
	if alive() {
		t.Fatal("the shortened limit never reached the running shell")
	}
	if reason, after, ok := m.Ended(owner, info.Handle); !ok || reason != ownershell.ReasonDetached || after != 10*time.Minute {
		t.Fatalf("ended: %q %s %v", reason, after, ok)
	}
	// The values are stored, and a fresh manager loads them.
	m2, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	a.shells = m2
	if err := a.refreshShellLimits(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := m2.Limits(); got != (ownershell.ShellLimits{Desktop: ownershell.Limits{Detached: 10 * time.Minute}, Cluster: ownershell.Limits{Detached: 5 * time.Minute, Idle: 2 * time.Hour}}) {
		t.Fatalf("stored limits: %+v", got)
	}

	// Review fix: null restores the runtime default, including the five-minute cluster grace
	// an administrator cannot type, and GET tells overrides from inherited values.
	if r := call(http.MethodPut, admin, `{"cluster":{"detached_seconds":3600}}`); r.Code != http.StatusOK {
		t.Fatalf("override cluster detached: %d %s", r.Code, r.Body)
	}
	if got := m2.Limits().Cluster.Detached; got != time.Hour {
		t.Fatalf("cluster detached %s, want 1h", got)
	}
	r = call(http.MethodPut, admin, `{"cluster":{"detached_seconds":null,"idle_seconds":null},"desktop":{"idle_seconds":null}}`)
	var reset struct {
		shellSettingsReply
		Stored map[string]map[string]*int64 `json:"stored"`
	}
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &reset) != nil {
		t.Fatalf("reset: %d %s", r.Code, r.Body)
	}
	if got := m2.Limits(); got.Cluster != (ownershell.Limits{Detached: 5 * time.Minute, Idle: 30 * time.Minute}) || got.Desktop.Detached != 10*time.Minute {
		t.Fatalf("after reset: %+v", got)
	}
	if reset.Cluster != (shellLimitPair{300, 1800}) || reset.Stored["cluster"]["detached_seconds"] != nil || reset.Stored["cluster"]["idle_seconds"] != nil || reset.Stored["desktop"]["idle_seconds"] != nil {
		t.Fatalf("reset reply: %+v %v", reset.shellSettingsReply, reset.Stored)
	}
	if v := reset.Stored["desktop"]["detached_seconds"]; v == nil || *v != 600 {
		t.Fatalf("an unrelated override changed: %v", v)
	}
	stored, _ := db.GetSettings(t.Context(), pool)
	if _, ok := stored[db.SettingShellClusterDetachedSeconds]; ok {
		t.Fatal("reset left the override stored")
	}
}

func TestShellSocketNamesTheInactivityLimitThatEndedIt(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAPI(NewHub(), nil, "", nil, "")
	a.coreAuth = newTestCoreAuthClient(t, pub, "shell-key")
	token := func(login string) string {
		return mintTestToken(t, key, "shell-key", identity.Claims{Kind: "human", Sub: "owner", Aud: coreAuthAudience, Sid: login, Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	}
	clock := &shellTestClock{now: time.Now()}
	binding := shellTestBinding()
	binding.Runtime = "cluster"
	rt := &shellTestRuntime{binding: binding, out: make(chan []byte, 8), input: make(chan []byte, 8)}
	m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	a.shells = m
	m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	t.Cleanup(func() { m.Unavailable(context.Background()) })
	mux := http.NewServeMux()
	a.RegisterShellRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	req := httptest.NewRequest(http.MethodPost, server.URL+"/api/sessions/session/shells", nil)
	req.Header.Set("Authorization", "Bearer "+token("login"))
	req.Header.Set("Origin", server.URL)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var info ownershell.Info
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &info) != nil {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	dial := func(login string) (*websocket.Conn, *http.Response, error) {
		d := websocket.Dialer{Subprotocols: []string{"bearer", token(login)}}
		return d.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/api/shells/"+info.Handle+"/socket", http.Header{"Origin": []string{server.URL}})
	}
	readEnded := func(conn *websocket.Conn) map[string]any {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		kind, b, err := conn.ReadMessage()
		var frame map[string]any
		if err != nil || kind != websocket.TextMessage || json.Unmarshal(b, &frame) != nil {
			t.Fatalf("no ended frame: %v %d %s", err, kind, b)
		}
		return frame
	}
	conn, res, err := dial("login")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	defer conn.Close()
	// Thirty minutes without typing while attached; the socket's heartbeats keep the lease.
	for end := clock.Now().Add(ownershell.IdleTimeout); clock.Now().Before(end); {
		clock.Add(10 * time.Second)
		if err := conn.WriteJSON(map[string]string{"type": "heartbeat"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		m.Tick(t.Context())
	}
	frame := readEnded(conn)
	if frame["type"] != "ended" || frame["reason"] != "idle" || frame["after_seconds"] != float64(1800) {
		t.Fatalf("live frame: %v", frame)
	}
	// A tab that reconnects later learns the same cause.
	again, res, err := dial("login")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	defer again.Close()
	if frame := readEnded(again); frame["type"] != "ended" || frame["reason"] != "idle" {
		t.Fatalf("reconnect frame: %v", frame)
	}
	// Another login of the same account gets the plain refusal, not the cause.
	if c, res, err := dial("other-login"); err == nil {
		_ = c.Close()
		t.Fatal("another login upgraded onto an ended shell")
	} else if res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("other login: %v", err)
	} else {
		_ = res.Body.Close()
	}
}

// Review fix: frames still queued when an inactivity limit retires the shell, and a heartbeat
// refused at the same moment, must not close the socket before the ended reason is sent.
func TestQueuedFramesCannotLoseTheEndedReason(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAPI(NewHub(), nil, "", nil, "")
	a.coreAuth = newTestCoreAuthClient(t, pub, "shell-key")
	tok := mintTestToken(t, key, "shell-key", identity.Claims{Kind: "human", Sub: "owner", Aud: coreAuthAudience, Sid: "login", Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	clock := &shellTestClock{now: time.Now()}
	binding := shellTestBinding()
	binding.Runtime = "cluster"
	rt := &shellTestRuntime{binding: binding, out: make(chan []byte, 8), input: make(chan []byte, 8)}
	m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	a.shells = m
	m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	t.Cleanup(func() { m.Unavailable(context.Background()) })
	l := m.Limits()
	l.Cluster.Detached = time.Hour // only the idle limit fires here
	m.SetLimits(l)
	mux := http.NewServeMux()
	a.RegisterShellRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	owner := ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}
	lost := 0
	for round := 0; round < 20; round++ {
		info, err := m.Open(t.Context(), owner, "session")
		if err != nil {
			t.Fatal(err)
		}
		// Ten seconds before the idle limit, with the grant renewed and no typing, the
		// browser attaches (a fresh heartbeat lease) as the runtime queues phases.
		for end := clock.Now().Add(ownershell.IdleTimeout - 10*time.Second); clock.Now().Before(end); {
			clock.Add(10 * time.Second)
			m.Tick(t.Context())
		}
		d := websocket.Dialer{Subprotocols: []string{"bearer", tok}}
		conn, res, err := d.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/api/shells/"+info.Handle+"/socket", http.Header{"Origin": []string{server.URL}})
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		frames := make(chan map[string]any, 64)
		go func() {
			defer close(frames)
			for {
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				kind, b, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var f map[string]any
				if kind == websocket.TextMessage && json.Unmarshal(b, &f) == nil {
					frames <- f
				}
			}
		}()
		if list, _ := m.List(t.Context(), owner, "session"); len(list) != 1 || !list[0].Attached {
			t.Fatalf("round %d: the socket is not attached", round)
		}
		_ = conn.WriteJSON(map[string]string{"type": "heartbeat"})
		for range 4 {
			m.NotifyPhase(t.Context(), "session", "running")
		}
		clock.Add(10 * time.Second)
		m.Tick(t.Context())
		_ = conn.WriteJSON(map[string]string{"type": "heartbeat"})
		got := false
		var seen []map[string]any
		for f := range frames {
			seen = append(seen, f)
			if f["type"] == "ended" && f["reason"] == "idle" {
				got = true
			}
		}
		if !got {
			r, _, ok := m.Ended(owner, info.Handle)
			t.Logf("round %d lost: ended=%q/%v frames=%v", round, r, ok, seen)
			lost++
		}
		_ = conn.Close()
	}
	if lost != 0 {
		t.Fatalf("%d of 20 sockets closed without the ended reason", lost)
	}
}

type countingConn struct {
	net.Conn
	read *atomic.Int64
}

func (c countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.read.Add(int64(n))
	return n, err
}

type countingListener struct {
	net.Listener
	read *atomic.Int64
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return countingConn{c, l.read}, nil
}

// endedHandleServer serves a manager whose only shell a detached limit has already ended, and
// reports how many bytes the server read and when each handler returned.
func endedHandleServer(t *testing.T) (url, handle, tok string, read *atomic.Int64, done chan time.Duration) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAPI(NewHub(), nil, "", nil, "")
	a.coreAuth = newTestCoreAuthClient(t, pub, "shell-key")
	tok = mintTestToken(t, key, "shell-key", identity.Claims{Kind: "human", Sub: "owner", Aud: coreAuthAudience, Sid: "login", Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	clock := &shellTestClock{now: time.Now()}
	binding := shellTestBinding()
	binding.Runtime = "cluster"
	rt := &shellTestRuntime{binding: binding, out: make(chan []byte, 8), input: make(chan []byte, 8)}
	m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	a.shells = m
	m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
	t.Cleanup(func() { m.Unavailable(context.Background()) })
	owner := ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}
	info, err := m.Open(t.Context(), owner, "session")
	if err != nil {
		t.Fatal(err)
	}
	for end := clock.Now().Add(ownershell.ReconnectGrace); clock.Now().Before(end); {
		clock.Add(10 * time.Second)
		m.Tick(t.Context())
	}
	if r, _, ok := m.Ended(owner, info.Handle); !ok || r != ownershell.ReasonDetached {
		t.Fatalf("setup: ended %q %v", r, ok)
	}
	mux := http.NewServeMux()
	a.RegisterShellRoutes(mux)
	done = make(chan time.Duration, 4)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mux.ServeHTTP(w, r)
		done <- time.Since(start)
	}))
	read = &atomic.Int64{}
	server.Listener = countingListener{server.Listener, read}
	server.Start()
	t.Cleanup(server.Close)
	return server.URL, info.Handle, tok, read, done
}

func dialEnded(t *testing.T, url, handle, tok string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{Subprotocols: []string{"bearer", tok}}
	conn, res, err := d.DialContext(t.Context(), "ws"+strings.TrimPrefix(url, "http")+"/api/shells/"+handle+"/socket", http.Header{"Origin": []string{url}})
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// A browser that never answers the close handshake on an already-ended handle cannot hold
// the handler beyond the drain's bound.
func TestEndedUpgradeCloseIsBoundedForASilentPeer(t *testing.T) {
	url, handle, tok, _, done := endedHandleServer(t)
	_ = dialEnded(t, url, handle, tok) // never reads, never answers the close
	select {
	case d := <-done:
		if d > 5*time.Second {
			t.Fatalf("handler held for %s", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler never returned for a silent peer")
	}
}

// Re-review: draining an already-ended upgrade keeps the shell socket's message-size cap, so a
// peer cannot make the server read (or buffer) an oversized message while it waits.
func TestEndedUpgradeDrainKeepsTheMessageSizeCap(t *testing.T) {
	url, handle, tok, read, done := endedHandleServer(t)
	conn := dialEnded(t, url, handle, tok)
	go func() { _ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 1<<20)) }()
	select {
	case d := <-done:
		if n := read.Load(); n > 256<<10 {
			t.Fatalf("server read %d bytes (handler %s) from an ended upgrade; the cap is %d", n, d, shellSocketReadLimit)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler never returned")
	}
}
