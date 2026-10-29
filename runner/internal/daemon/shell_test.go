//go:build linux

package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"
)

func TestOwnerShellPodHelperProcess(t *testing.T) {
	if len(os.Args) > 2 && os.Args[len(os.Args)-2] == "p5-pod-helper" && os.Args[len(os.Args)-1] == "--probe" {
		if shellruntime.Probe(os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "p5-pod-helper" {
		if shellruntime.ServeHelper(os.Stdin, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

// The pod/cluster service uses the SAME authenticated outbound WebSocket client
// as agent and file traffic. This is a fake pod/server connection, not a live pod.
func TestOwnerShellClusterOverExistingOutboundConnection(t *testing.T) {
	if !protocol.WorkspaceRuntimeEnabled("cluster", "linux") {
		t.Fatal("released cluster shell must be enabled in ordinary builds")
	}

	cwd := t.TempDir()
	i, err := shellruntime.ResolveIdentity(cwd)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err = unix.Stat(cwd, &stat); err != nil {
		t.Fatal(err)
	}
	b := ownershell.Binding{RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: stat.Dev, Inode: stat.Ino}, ManifestDigest: "digest", Session: "pod-session", Owner: "owner", Launch: "launch", Connection: "connection", Audience: ownershell.RuntimeAudience("connection", 1), Runtime: "cluster", Live: true, ServerCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, Sandbox: true, Cluster: true}, RuntimeCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, Sandbox: true, Cluster: true}}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	grant := ownershell.SignGrant(key, protocol.ShellGrant{Version: 1, Audience: b.Audience, OwnerID: b.Owner, LoginID: "login", SessionID: b.Session, LaunchGeneration: b.Launch, ConnectionID: b.Connection, ShellGeneration: "shell", Runtime: b.Runtime, PolicyRevision: 1, IssuedAt: now, ExpiresAt: now + 30})
	result := make(chan error, 1)
	upgrade := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := b
		grant := grant
		conn, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var hello protocol.DaemonHello
		if err = conn.ReadJSON(&hello); err != nil {
			result <- err
			return
		}
		if hello.Token != "pod-connection-credential" {
			result <- shellruntime.ErrClosed
			return
		}
		if err = conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome", DaemonID: "connection", SessionID: b.Session, WorkspaceCapabilities: b.ServerCapabilities}); err != nil {
			result <- err
			return
		}
		var output string
		rpc := func(req shellruntime.RPC) (shellruntime.RPC, error) {
			req.Version = 1
			req.Type = "owner_shell_request"
			if req.SessionID == "" {
				req.SessionID = b.Session
			}
			if err := conn.WriteJSON(req); err != nil {
				return shellruntime.RPC{}, err
			}
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					return shellruntime.RPC{}, err
				}
				var reply shellruntime.RPC
				_ = json.Unmarshal(raw, &reply)
				if reply.Type == "owner_shell_output" {
					output += string(reply.Data)
				}
				if reply.RequestID == req.RequestID {
					return reply, nil
				}
			}
		}
		description, err := rpc(shellruntime.RPC{RequestID: "describe", Op: "describe"})
		if err != nil || !description.OK {
			result <- shellruntime.ErrClosed
			return
		}
		b = description.Binding
		grant.Grant.ConnectionID = b.Connection
		grant.Grant.Audience = b.Audience
		grant = ownershell.SignGrant(key, grant.Grant)
		config := shellruntime.RPC{RequestID: "configure", Op: "configure", Binding: b, Key: pub, Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}
		if reply, e := rpc(config); e != nil || !reply.OK {
			result <- shellruntime.ErrClosed
			return
		}
		if reply, e := rpc(shellruntime.RPC{RequestID: "foreign", SessionID: "foreign-session", Op: "open", Grant: grant}); e != nil || reply.OK {
			result <- shellruntime.ErrClosed
			return
		}
		if reply, e := rpc(shellruntime.RPC{RequestID: "open", Op: "open", Grant: grant}); e != nil || !reply.OK {
			result <- shellruntime.ErrClosed
			return
		}
		if reply, e := rpc(shellruntime.RPC{RequestID: "input", Op: "input", Grant: grant, Seq: 1, Data: []byte("printf '\\120\\117\\104'\n")}); e != nil || !reply.OK {
			result <- shellruntime.ErrClosed
			return
		}
		for !strings.Contains(output, "POD") {
			var reply shellruntime.RPC
			if err = conn.ReadJSON(&reply); err != nil {
				result <- err
				return
			}
			if reply.Type == "owner_shell_output" {
				output += string(reply.Data)
			}
		}
		reply, err := rpc(shellruntime.RPC{RequestID: "close", Op: "close_launch", Launch: ownershell.SupervisorIdentity(b, grant).Launch})
		if err != nil || !reply.OK || reply.Cleanup == nil || reply.Cleanup.ValidateFor(ownershell.SupervisorIdentity(b, grant).Launch, "") != nil {
			result <- shellruntime.ErrClosed
			return
		}
		result <- nil
	}))
	defer server.Close()
	client := NewWSClient(Config{WorkspaceCapabilities: b.RuntimeCapabilities, ServerURL: "ws" + strings.TrimPrefix(server.URL, "http"), DaemonToken: "pod-connection-credential", DaemonMode: "runner", DaemonName: "runner-pod-session", HeartbeatInterval: time.Hour})
	service := client.OwnerShellRuntime()
	fresh := true
	if err = service.Register(t.Context(), new(sync.Mutex), b, shellruntime.Command{Executable: os.Args[0], Args: []string{"-test.run=^TestOwnerShellPodHelperProcess$", "--", "p5-pod-helper"}, Identity: i}, func(context.Context) error {
		if fresh {
			fresh = false
			return nil
		}
		return shellruntime.ErrClosed
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); client.Run(ctx, func() []string { return []string{b.Session} }) }()
	defer func() { cancel(); <-done }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("outbound pod shell stalled")
	}
}
