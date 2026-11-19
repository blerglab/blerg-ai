//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"golang.org/x/sys/unix"
)

// Follow the bundled executable's real internal entry points even when its
// image includes this test harness. The keeper and daemon client must execute
// the same image; a separate test client image is intentionally refused.
func TestMain(m *testing.M) {
	// Probe subprocesses run this test image through the scrubbed production
	// environment. Reexec only the test probe to remove the race exit pause.
	if len(os.Args) == 4 && os.Args[1] == "shell-supervisor" && os.Args[2] == "--keeper-restart" && (os.Args[3] == "--probe" || os.Args[3] == "--probe-persistence") && os.Getenv("GORACE") != "atexit_sleep_ms=0" {
		if unix.Exec("/proc/self/exe", os.Args, append(os.Environ(), "GORACE=atexit_sleep_ms=0")) != nil {
			os.Exit(93)
		}
	}
	if len(os.Args) > 1 && (os.Args[1] == "shell-keeper" || os.Args[1] == "shell-supervisor") {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Uses its own real tracer. Run through /tmp/cx/outside.sh.
func TestBundledKeeperDeathKillsOwnedTracerAndShell(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0") // probes execute the race test image, not a release binary
	scratch, err := os.MkdirTemp("", "keeper-bundled-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	state := filepath.Join(scratch, "state")
	command := func(action string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "shell-keeper", action)
		cmd.Env = append(os.Environ(), "BLERG_RUNNER_DAEMON_STATE_DIR="+state, "BLERG_HOST_SHELL_CGROUP=0", "NOTIFY_SOCKET=")
		return cmd
	}
	if out, err := command("init").CombinedOutput(); err != nil {
		t.Fatalf("bundled init: %v %s", err, out)
	}
	notify := filepath.Join(scratch, "notify")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: notify, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	keeper := command("serve")
	keeper.Env = append(keeper.Env, "NOTIFY_SOCKET="+notify)
	if err = keeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = keeper.Process.Kill(); _ = keeper.Wait() }()
	_ = listener.SetReadDeadline(time.Now().Add(5 * time.Second))
	ready := make([]byte, 128)
	n, _, err := listener.ReadFromUnix(ready)
	if err != nil || !bytes.Contains(ready[:n], []byte("READY=1")) {
		t.Fatal("bundled keeper did not announce readiness", err)
	}
	dir := filepath.Join(state, "shell-keeper")
	authority, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	view, err := shellkeeper.InspectSnapshot(t.Context(), dir, authority.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := shellruntime.ConnectKeeper(t.Context(), dir, authority.Epoch, view.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	frames := make(chan shellruntime.RPC, 128)
	service := shellruntime.NewService(func(r shellruntime.RPC) error {
		select {
		case frames <- r:
			return nil
		default:
			return shellruntime.ErrClosed
		}
	})
	service.SetHostBridge(bridge)
	caps := protocol.WorkspaceCapabilities{ManifestVersion: protocol.WorkspaceVersion, ShellVersion: 1, LinuxHost: true, ShellPersistence: 2, PersistentHost: true}
	service.SetCapabilities(caps, caps)
	if err = service.Rebind(t.Context()); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(scratch, "work")
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := shellruntime.ResolvePersistentIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	b := ownershell.Binding{Session: "bundled-session", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Root: identity.MemberRoot, ManifestDigest: "fixture-manifest", Connection: "controller", Live: true}
	hello := service.ShellHello()
	welcome := protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: b.Owner, TokenID: "fixture-credential", Revision: 1, ExpiresAt: time.Now().Add(8 * 24 * time.Hour)}}
	if err = service.AcceptShellLink(t.Context(), hello, &welcome); err != nil {
		t.Fatal(err)
	}
	host := shellruntime.HostCommand("unused-client-path", identity)
	host.Root, host.PublicRoot = identity.MemberRoot, root
	if err = service.Register(t.Context(), new(sync.Mutex), b, host, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, err = service.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(r shellruntime.RPC) shellruntime.RPC {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		r.Version = 2
		r.SessionID = b.Session
		reply, err := service.Call(ctx, r)
		if err != nil {
			t.Fatalf("bundled %s refused: %v reason=%s stage=%s", r.Op, err, reply.ErrorCode, reply.StartCause)
		}
		return reply
	}
	call(shellruntime.RPC{Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}})
	actor := ownershell.Actor{Kind: "human", Owner: b.Owner, Login: "login"}
	consent := call(shellruntime.RPC{Op: "consent_challenge", Actor: actor})
	accepted := call(shellruntime.RPC{Op: "consent_accept", Actor: actor, PersistentConsent: consent.PersistentConsent})
	now := time.Now()
	custody := protocol.ShellCustody{Version: 2, Owner: b.Owner, Login: "login", Session: b.Session, Launch: b.Launch, Runtime: "host", RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Desktop: "desktop", Credential: welcome.Desktop.TokenID, OwnershipRevision: 1, Runner: "runner", Daemon: hello.Daemon, Shell: "bundled-shell", Nonce: "fixture-custody", Identity: shellruntime.RuntimeIdentityDigest(identity), Consent: accepted.PersistentConsent.Host.ID, Created: now.Unix(), LifetimeUntil: now.Add(time.Hour).Unix()}
	grant := ownershell.SignGrant(key, protocol.ShellGrant{Version: 2, OwnerID: b.Owner, LoginID: "login", SessionID: b.Session, LaunchGeneration: b.Launch, Runtime: "host", ConnectionID: b.Connection, Audience: b.Audience, ShellGeneration: custody.Shell, ConsentID: custody.Consent, CustodyID: custody.Digest(), AccessEpoch: 1, PolicyRevision: 1, IssuedAt: now.Unix(), ExpiresAt: now.Add(30 * time.Second).Unix()})
	call(shellruntime.RPC{Op: "open", Custody: &custody, Grant: grant, Window: ownershell.ServerOutputWindow})
	call(shellruntime.RPC{Op: "input", Grant: grant, Seq: 1, Data: []byte("printf 'BUNDLED_PID=%s\\n' \"$$\"\n")})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var output []byte
	shellPID := 0
	for shellPID == 0 {
		select {
		case r := <-frames:
			if r.Type != "owner_shell_output" {
				continue
			}
			output = append(output, r.Data...)
			end := r.Offset + uint64(len(r.Data))
			call(shellruntime.RPC{Op: "credit", Grant: grant, Offset: end})
			call(shellruntime.RPC{Op: "receipt", Grant: grant, Offset: end})
			if match := regexp.MustCompile(`BUNDLED_PID=([0-9]+)`).FindSubmatch(output); len(match) == 2 {
				shellPID, _ = strconv.Atoi(string(match[1]))
			}
		case <-ctx.Done():
			t.Fatal("bundled shell did not start")
		}
	}
	parentPID := func(pid int) int {
		t.Helper()
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if value, ok := strings.CutPrefix(line, "PPid:"); ok {
				parent, _ := strconv.Atoi(strings.TrimSpace(value))
				return parent
			}
		}
		t.Fatal("missing parent identity")
		return 0
	}
	tracerPID := parentPID(shellPID)
	if parentPID(tracerPID) != keeper.Process.Pid {
		t.Fatal("per-shell tracer was not keeper-parented")
	}
	shellFD, err := unix.PidfdOpen(shellPID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(shellFD) }()
	tracerFD, err := unix.PidfdOpen(tracerPID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(tracerFD) }()
	if err = keeper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = keeper.Wait()
	for _, fd := range []int{tracerFD, shellFD} {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if n, err := unix.Poll(poll, 5000); err != nil || n != 1 {
			t.Fatal("keeper death left a tracer or traced shell alive", err)
		}
	}
	if err = service.HostCustodyReady(t.Context(), b.Session); err == nil {
		t.Fatal("keeper death became proof of reconciled custody")
	}
}
