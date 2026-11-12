//go:build linux

package main

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
)

type keeperTurnProvider struct{ entered, release chan struct{} }

func (p *keeperTurnProvider) Stream(ctx context.Context, _ agent.Request) (<-chan agent.StreamEvent, error) {
	close(p.entered)
	out := make(chan agent.StreamEvent)
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			return
		case <-p.release:
		}
		for _, ev := range []agent.StreamEvent{{Kind: "block", Block: &agent.Block{Type: "text", Text: "turn survived"}}, {Kind: "done", StopReason: "end_turn", Usage: &agent.Usage{}}} {
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func TestKeeperLossLiveAgentProcessFixture(t *testing.T) {
	if os.Getenv("BLERG_KEEPER_LOSS_FIXTURE") != "1" {
		return
	}
	done, err := keeperStartup()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	refreshes, turnReceived, upstreamReady := make(chan string, 8), make(chan struct{}), make(chan struct{})
	var turnOnce, upstreamOnce sync.Once
	var connections atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
	caps.ShellPersistence = 2
	caps.PersistentHost = true
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		connections.Add(1)
		for {
			var frame struct {
				Type  string
				Kind  string
				Hello *protocol.ShellLinkHello `json:"shell_hello"`
				Link  *protocol.ShellLinkHello `json:"shell_link"`
			}
			if conn.ReadJSON(&frame) != nil {
				return
			}
			if frame.Type == "agent_event" && frame.Kind == "turn_done" {
				turnOnce.Do(func() { close(turnReceived) })
				continue
			}
			var reply any
			if frame.Type == "daemon_hello" {
				reply = protocol.DaemonWelcome{Type: "daemon_welcome", DaemonID: "fixture", WorkspaceCapabilities: caps}
			}
			if frame.Type == "daemon_shell_hello" && frame.Hello != nil {
				hello := *frame.Hello
				welcome := protocol.ShellLinkWelcome{Version: 2, Runner: "fixture", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "fixture", Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}}
				reply = struct {
					Type  string                    `json:"type"`
					Hello protocol.ShellLinkHello   `json:"shell_hello"`
					Link  protocol.ShellLinkWelcome `json:"shell_link"`
				}{"daemon_shell_welcome", hello, welcome}
			}
			if reply != nil {
				if conn.WriteJSON(reply) != nil {
					return
				}
				if frame.Type == "daemon_hello" {
					upstreamOnce.Do(func() { close(upstreamReady) })
				}
				if frame.Type == "daemon_shell_hello" {
					select {
					case refreshes <- frame.Hello.Daemon:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}))
	defer core.Close()
	client := daemon.NewWSClient(daemon.Config{ServerURL: "ws" + strings.TrimPrefix(core.URL, "http"), DaemonToken: "fixture", DaemonName: "fixture", DaemonMode: "local", ReposRoot: os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR"), HeartbeatInterval: time.Hour, WorkspaceCapabilities: caps})
	service := client.OwnerShellRuntimeContext(ctx)
	runDone := make(chan struct{})
	go func() { client.Run(ctx, func() []string { return nil }); close(runDone) }()
	defer func() { cancel(); <-runDone }()
	select {
	case <-upstreamReady:
	case <-time.After(4 * time.Second):
		t.Fatal("keeper outage prevented agent server connection")
	}
	var initialGeneration string
	if os.Getenv("BLERG_KEEPER_MISSING_FIXTURE") != "1" {
		select {
		case initialGeneration = <-refreshes:
		case <-time.After(6 * time.Second):
			t.Fatal("keeper connection never authenticated")
		}
		ready, stop := context.WithTimeout(ctx, 6*time.Second)
		if err := service.WaitHostKeeper(ready); err != nil {
			stop()
			t.Fatal("live turn started without a fully ready keeper", err)
		}
		stop()
	}
	root := os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR")
	identity, err := shellruntime.ResolvePersistentIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	command := shellruntime.HostCommand("/proc/self/exe", identity)
	command.PublicRoot = root
	command.Root = identity.MemberRoot
	binding := ownershell.Binding{Session: "keeper-live-turn", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Connection: "connection", Live: true, Root: identity.MemberRoot, ManifestDigest: "manifest"}
	if err := service.Register(ctx, new(sync.Mutex), binding, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal("keeper absence blocked agent workspace registration", err)
	}
	p := &keeperTurnProvider{make(chan struct{}), make(chan struct{})}
	finished := make(chan struct{})
	host := daemon.NewAgentHost(client, daemon.AgentHostConfig{Provider: p, ReposRoot: os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR"), HomeDir: os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR"), OnTurnDone: func(string) { close(finished) }})
	defer host.Kill("keeper-live-turn")
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "keeper-live-turn", Kind: "agent", ProjectPath: os.Getenv("BLERG_RUNNER_DAEMON_STATE_DIR"), InitialPrompt: "complete this turn"})
	select {
	case <-p.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("agent never entered live turn")
	}
	fmt.Fprintln(os.Stdout, "turn-running")
	var release [1]byte
	if _, err := os.Stdin.Read(release[:]); err != nil {
		t.Fatal(err)
	}
	guard, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	if err := service.HostCustodyReady(guard, "keeper-live-turn"); err == nil {
		t.Fatal("keeper loss failed to refuse host shells")
	}
	close(p.release)
	select {
	case <-finished:
	case <-time.After(4 * time.Second):
		t.Fatal("keeper failure interrupted agent turn")
	}
	select {
	case <-turnReceived:
	case <-time.After(4 * time.Second):
		t.Fatal("agent turn result did not reach the server")
	}
	if connections.Load() != 1 {
		t.Fatal("keeper failure replaced shared agent connection")
	}
	fmt.Fprintln(os.Stdout, "turn-finished")
	restored, stopRestore := context.WithTimeout(ctx, 6*time.Second)
	defer stopRestore()
	for {
		select {
		case generation := <-refreshes:
			if generation == initialGeneration {
				continue
			}
			if err := service.WaitHostKeeper(restored); err != nil {
				t.Fatal("keeper never published fully verified shell readiness", err)
			}
			if err := service.HostCustodyReady(restored, "keeper-live-turn"); err != nil {
				t.Fatal("keeper returned but host shells stayed fenced", err)
			}
			reply, err := service.Call(restored, shellruntime.RPC{Version: 1, Type: "owner_shell_request", SessionID: "keeper-live-turn", Op: "describe"})
			if err != nil || !reply.OK || reply.Binding.ShellLink.Version != 2 || !reply.Binding.RuntimeCapabilities.SupportsPersistence("host") {
				t.Fatal("replacement keeper did not restore persistent host shell admission", err, "ok", reply.OK, "code", reply.ErrorCode, "link", reply.Binding.ShellLink.Version, "runtime_persistence", reply.Binding.RuntimeCapabilities.SupportsPersistence("host"), "server_persistence", reply.Binding.ServerCapabilities.SupportsPersistence("host"))
			}
			if connections.Load() != 1 {
				t.Fatal("keeper recovery replaced agent connection")
			}
			fmt.Fprintln(os.Stdout, "shell-restored")
			return
		case <-restored.Done():
			t.Fatal("daemon never reauthenticated replacement keeper")
		}
	}
}

func TestKeeperLossAndMissingStateNeverEndLiveAgentTurn(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint("missing_", missing), func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "shell-keeper")
			scratch, err := os.MkdirTemp("", "keeper-live-turn-")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(scratch) }()
			notify := filepath.Join(scratch, "notify")
			listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: notify, Net: "unixgram"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			startKeeper := func() *exec.Cmd {
				t.Helper()
				keeper := exec.CommandContext(t.Context(), os.Args[0], "shell-keeper", "serve")
				keeper.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0", "BLERG_RUNNER_DAEMON_STATE_DIR="+root, "BLERG_HOST_SHELL_CGROUP=0", "NOTIFY_SOCKET="+notify)
				if err := keeper.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = keeper.Process.Kill(); _ = keeper.Wait() })
				_ = listener.SetReadDeadline(time.Now().Add(4 * time.Second))
				var ready [128]byte
				if _, _, err := listener.ReadFromUnix(ready[:]); err != nil {
					t.Fatal("keeper never started", err)
				}
				return keeper
			}
			var keeper *exec.Cmd
			if !missing {
				if err := shellkeeper.Initialize(dir); err != nil {
					t.Fatal(err)
				}
				keeper = startKeeper()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 18*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKeeperLossLiveAgentProcessFixture$")
			child.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0", "BLERG_KEEPER_LOSS_FIXTURE=1", "BLERG_RUNNER_DAEMON_STATE_DIR="+root)
			if missing {
				child.Env = append(child.Env, "BLERG_KEEPER_MISSING_FIXTURE=1")
			}
			out, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			in, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = in.Close() }()
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
			scanner := bufio.NewScanner(out)
			if !scanner.Scan() || scanner.Text() != "turn-running" {
				t.Fatal("agent did not start independently")
			}
			if !missing {
				if err = keeper.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = keeper.Wait()
			}
			if _, err = in.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			if !scanner.Scan() || scanner.Text() != "turn-finished" {
				t.Fatal("live turn died with keeper")
			}
			// The turn must finish while the keeper is still down. Only then
			// restore it and prove shell-only recovery on the same daemon connection.
			if missing {
				if err := shellkeeper.Initialize(dir); err != nil {
					t.Fatal(err)
				}
			}
			_ = startKeeper()
			if !scanner.Scan() || scanner.Text() != "shell-restored" {
				line := scanner.Text()
				if scanner.Scan() {
					line += " " + scanner.Text()
				}
				t.Fatal("keeper replacement did not restore host shells", line)
			}
			if err = child.Wait(); err != nil {
				t.Fatal("daemon exited on keeper loss", err)
			}
		})
	}
}

func TestMainKeeperFailureCannotExitOrSweepLiveCustody(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			return true
		}
		for i, statement := range fn.Body.List {
			assignment, ok := statement.(*ast.AssignStmt)
			if !ok {
				continue
			}
			for _, value := range assignment.Rhs {
				call, ok := value.(*ast.CallExpr)
				if !ok {
					continue
				}
				name, ok := call.Fun.(*ast.Ident)
				if !ok || name.Name != "keeperStartup" {
					continue
				}
				found = true
				if i+1 >= len(fn.Body.List) {
					t.Fatal("keeper startup failure handling missing")
				}
				ast.Inspect(fn.Body.List[i+1], func(n ast.Node) bool {
					if selector, ok := n.(*ast.SelectorExpr); ok {
						if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "os" && selector.Sel.Name == "Exit" {
							t.Error("keeper startup failure can exit the daemon")
						}
					}
					return true
				})
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && (id.Name == "sweepShellCgroups" || id.Name == "SweepShellCgroups") {
				t.Error("agent startup sweeps custody without keeper.lock")
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatal("agent startup lost its shell-only keeper boundary")
	}
}
