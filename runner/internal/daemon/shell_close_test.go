//go:build linux

package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"
)

// slowOnceConn delays one armed Write by up to delay, as brief congestion
// would, while honoring write deadlines (including the writer's interrupt).
type slowOnceConn struct {
	net.Conn
	armed    atomic.Bool
	entered  chan struct{}
	delay    time.Duration
	mu       sync.Mutex
	deadline time.Time
	ended    *atomic.Int64 // when the delayed write finished
	// match picks the write to delay: only a frame whose payload it accepts.
	match func(payload []byte) bool
}

// framePayloadStart unmasks the start of a client WebSocket frame. gorilla
// writes each frame, header included, in one Write, so a message's first Write
// begins with its frame header.
func framePayloadStart(b []byte) []byte {
	if len(b) < 2 || b[1]&0x80 == 0 {
		return nil
	}
	offset := 2
	switch b[1] & 0x7f {
	case 126:
		offset += 2
	case 127:
		offset += 8
	}
	if len(b) < offset+4 {
		return nil
	}
	mask := b[offset : offset+4]
	payload := b[offset+4:]
	out := make([]byte, min(len(payload), 256))
	for i := range out {
		out[i] = payload[i] ^ mask[i%4]
	}
	return out
}

func (c *slowOnceConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(t)
}
func (c *slowOnceConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return c.Conn.SetDeadline(t)
}
func (c *slowOnceConn) Write(b []byte) (int, error) {
	if c.armed.Load() && (c.match == nil || c.match(framePayloadStart(b))) && c.armed.CompareAndSwap(true, false) {
		close(c.entered)
		until := time.Now().Add(c.delay)
		for time.Now().Before(until) {
			c.mu.Lock()
			d := c.deadline
			c.mu.Unlock()
			if !d.IsZero() && !time.Now().Before(d) {
				return 0, os.ErrDeadlineExceeded
			}
			time.Sleep(time.Millisecond)
		}
		n, err := c.Conn.Write(b)
		if c.ended != nil {
			c.ended.Store(time.Now().UnixNano())
		}
		return n, err
	}
	return c.Conn.Write(b)
}

// Closing a shell while one of its output frames is being written must not
// cost anyone else the shared desktop connection. The shell's cancellation
// must land while that frame is still being written (asserted), the frame
// finishes under the writer's own deadline, the close receipt arrives after
// the shell's output is joined, nothing for the shell follows it, and a second
// live session on the same socket keeps working (control and data).
func TestClosingBusyShellKeepsSharedConnection(t *testing.T) {
	cwd := t.TempDir()
	i, err := shellruntime.ResolveIdentity(cwd)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err = unix.Stat(cwd, &stat); err != nil {
		t.Fatal(err)
	}
	caps := protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, Sandbox: true, Cluster: true}
	binding := func(session, launch string) ownershell.Binding {
		return ownershell.Binding{RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: stat.Dev, Inode: stat.Ino}, ManifestDigest: "digest", Session: session, Owner: "owner", Launch: launch, Connection: "connection", Audience: ownershell.RuntimeAudience("connection", 1), Runtime: "cluster", Live: true, ServerCapabilities: caps, RuntimeCapabilities: caps}
	}
	busy, other := binding("busy-session", "launch-busy"), binding("other-session", "launch-other")
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	grantFor := func(b ownershell.Binding, shell string) ownershell.SignedGrant {
		now := time.Now().Unix()
		return ownershell.SignGrant(key, protocol.ShellGrant{Version: 1, Audience: b.Audience, OwnerID: b.Owner, LoginID: "login", SessionID: b.Session, LaunchGeneration: b.Launch, ConnectionID: b.Connection, ShellGeneration: shell, Runtime: b.Runtime, PolicyRevision: 1, IssuedAt: now, ExpiresAt: now + 30})
	}

	const delay = 600 * time.Millisecond // inside the writer's one-second deadline
	// Delay only the closing shell's own output frame, never the other live session's.
	slow := &slowOnceConn{entered: make(chan struct{}), delay: delay, match: func(payload []byte) bool {
		return bytes.Contains(payload, []byte(`"type":"owner_shell_output","session_id":"busy-session"`))
	}}
	var writeEnded atomic.Int64
	old := serverDialer
	serverDialer = &websocket.Dialer{HandshakeTimeout: 5 * time.Second, NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		slow.Conn = conn
		slow.ended = &writeEnded
		return slow, nil
	}}
	t.Cleanup(func() { serverDialer = old })

	var client *WSClient
	var canceledAt atomic.Int64 // when the busy shell's output lifetime ended
	result := make(chan string, 1)
	upgrade := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(why string) { result <- why }
		conn, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			fail("upgrade")
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		var hello protocol.DaemonHello
		if conn.ReadJSON(&hello) != nil || conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome", DaemonID: "connection", SessionID: busy.Session, WorkspaceCapabilities: caps}) != nil {
			fail("handshake")
			return
		}
		var busyGrant ownershell.SignedGrant
		var proved bool
		var otherOutput string
		read := func() (shellruntime.RPC, error) {
			_, raw, err := conn.ReadMessage()
			var frame shellruntime.RPC
			if err == nil {
				_ = json.Unmarshal(raw, &frame)
			}
			if err == nil && frame.Type == "owner_shell_output" {
				if proved && frame.SessionID == busy.Session {
					return frame, errors.New("busy shell output after its cleanup proof")
				}
				if frame.SessionID == other.Session {
					otherOutput += string(frame.Data)
				}
			}
			return frame, err
		}
		rpc := func(req shellruntime.RPC) (shellruntime.RPC, error) {
			req.Version, req.Type = 1, "owner_shell_request"
			if err := conn.WriteJSON(req); err != nil {
				return shellruntime.RPC{}, err
			}
			for {
				reply, err := read()
				if err != nil || reply.RequestID == req.RequestID {
					return reply, err
				}
			}
		}
		open := func(b ownershell.Binding, shell string) (ownershell.SignedGrant, ownershell.Binding, bool) {
			description, err := rpc(shellruntime.RPC{SessionID: b.Session, RequestID: "describe-" + shell, Op: "describe"})
			if err != nil || !description.OK {
				return ownershell.SignedGrant{}, b, false
			}
			b = description.Binding
			g := grantFor(b, shell)
			if reply, e := rpc(shellruntime.RPC{SessionID: b.Session, RequestID: "configure-" + shell, Op: "configure", Binding: b, Key: pub, Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}); e != nil || !reply.OK {
				return g, b, false
			}
			reply, e := rpc(shellruntime.RPC{SessionID: b.Session, RequestID: "open-" + shell, Op: "open", Grant: g})
			return g, b, e == nil && reply.OK
		}
		busyGrant, busyBinding, ok := open(busy, "busy-shell")
		if !ok {
			fail("open busy shell")
			return
		}
		otherGrant, _, ok := open(other, "other-shell")
		if !ok {
			fail("open second session's shell")
			return
		}
		if reply, e := rpc(shellruntime.RPC{SessionID: busy.Session, RequestID: "input", Op: "input", Grant: busyGrant, Seq: 1, Data: []byte("seq 1 1000000\n")}); e != nil || !reply.OK {
			fail("input")
			return
		}
		for seen := 0; seen < 4; {
			frame, err := read()
			if err != nil {
				fail("output: " + err.Error())
				return
			}
			if frame.Type == "owner_shell_output" && frame.SessionID == busy.Session {
				seen++
			}
		}
		slow.armed.Store(true)
		select {
		case <-slow.entered:
		case <-time.After(5 * time.Second):
			fail("no output write was delayed")
			return
		}
		writeStarted := time.Now()
		closeSent := time.Now()
		if err := conn.WriteJSON(shellruntime.RPC{Version: 1, Type: "owner_shell_request", SessionID: busy.Session, RequestID: "close", Op: "close", Grant: busyGrant}); err != nil {
			fail("close request")
			return
		}
		var receipt *shellruntime.RPC
		for receipt == nil {
			frame, err := read()
			if err != nil {
				// Say whether the delayed (busy-shell) frame's own sender was canceled
				// while that frame was being written: the failure this guards against.
				overlap := canceledAt.Load() != 0 && time.Unix(0, canceledAt.Load()).After(writeStarted) && (writeEnded.Load() == 0 || time.Unix(0, canceledAt.Load()).Before(time.Unix(0, writeEnded.Load())))
				fail(fmt.Sprintf("lost before the close receipt (busy-shell sender canceled during its delayed frame: %v): %v", overlap, err))
				return
			}
			if frame.RequestID == "close" {
				receipt = &frame
			}
		}
		proved = true
		received := time.Now()
		if !receipt.OK || receipt.Cleanup == nil || receipt.Cleanup.ValidateFor(ownershell.SupervisorIdentity(busyBinding, busyGrant).Launch, busyGrant.Grant.ShellGeneration) != nil {
			fail("close receipt without a cleanup proof")
			return
		}
		if received.Sub(closeSent) > 2*time.Second {
			fail("close took " + received.Sub(closeSent).String())
			return
		}
		// The shell's cancellation landed while its frame was being written,
		// and the receipt (the proof) came after that write finished.
		canceled, ended := time.Unix(0, canceledAt.Load()), time.Unix(0, writeEnded.Load())
		if canceledAt.Load() == 0 || writeEnded.Load() == 0 || canceled.Before(writeStarted) || !canceled.Before(ended) {
			fail("shell cancellation did not overlap the delayed write")
			return
		}
		if ended.After(received) {
			fail("close receipt preceded the committed output write")
			return
		}
		// The second session keeps working on the same socket: control, then data.
		if reply, e := rpc(shellruntime.RPC{SessionID: other.Session, RequestID: "resize", Op: "resize", Grant: otherGrant, Seq: 1, Cols: 100, Rows: 30}); e != nil || !reply.OK {
			fail("second session control after the close")
			return
		}
		if reply, e := rpc(shellruntime.RPC{SessionID: other.Session, RequestID: "other-input", Op: "input", Grant: otherGrant, Seq: 1, Data: []byte("printf '\\117\\124\\110\\105\\122'\n")}); e != nil || !reply.OK {
			fail("second session input after the close")
			return
		}
		for !strings.Contains(otherOutput, "OTHER") {
			if _, err := read(); err != nil {
				fail("second session output: " + err.Error())
				return
			}
		}
		// Observe a little longer: still nothing for the closed shell.
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		for {
			if _, err := read(); err != nil {
				if strings.Contains(err.Error(), "after its cleanup proof") {
					fail(err.Error())
					return
				}
				break
			}
		}
		result <- ""
	}))
	defer server.Close()
	client = NewWSClient(Config{WorkspaceCapabilities: caps, ServerURL: "ws" + strings.TrimPrefix(server.URL, "http"), DaemonToken: "pod-connection-credential", DaemonMode: "runner", DaemonName: "runner-busy-session", HeartbeatInterval: time.Hour})
	service := client.OwnerShellRuntime()
	// The production sender, observed: record when the busy shell's lifetime ends.
	var observed atomic.Bool
	service.SetOutputSender(func(life context.Context, frame shellruntime.RPC) error {
		if frame.SessionID == busy.Session && observed.CompareAndSwap(false, true) {
			go func() {
				<-life.Done()
				canceledAt.Store(time.Now().UnixNano())
			}()
		}
		return client.sendShellOutput(life, frame)
	})
	// Helpers exit without the race runtime's one-second exit sleep, so the
	// shell's cancellation is timed by the close, not by an instrumentation artifact.
	command := shellruntime.Command{Executable: "/usr/bin/env", Args: []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestOwnerShellPodHelperProcess$", "--", "p5-pod-helper"}, Identity: i}
	for _, b := range []ownershell.Binding{busy, other} {
		fresh := true
		if err = service.Register(t.Context(), new(sync.Mutex), b, command, func(context.Context) error {
			if fresh {
				fresh = false
				return nil
			}
			return shellruntime.ErrClosed
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx, func() []string { return []string{busy.Session, other.Session} })
	}()
	defer func() { cancel(); <-done }()
	select {
	case why := <-result:
		if why != "" {
			t.Fatal(why)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("stalled")
	}
}
