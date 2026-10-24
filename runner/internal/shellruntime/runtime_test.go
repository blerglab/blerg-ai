//go:build linux

package shellruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"golang.org/x/sys/unix"
)

func TestRuntimeHelperProcess(t *testing.T) {
	if len(os.Args) > 2 && os.Args[len(os.Args)-2] == "p5-identity-helper" && os.Args[len(os.Args)-1] == "--probe" {
		if Probe(os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Args[len(os.Args)-1] == "p5-identity-helper" {
		calls := 0
		err := serveHelper(os.Stdin, os.Stdout, func(cwd string) (Identity, error) {
			i, err := ResolveIdentity(cwd)
			calls++
			if calls > 1 {
				i.UID++
			}
			return i, err
		})
		if errors.Is(err, ErrIdentityChanged) {
			os.Exit(3)
		}
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 2 && os.Args[len(os.Args)-2] == "p5-shell-helper" && os.Args[len(os.Args)-1] == "--probe" {
		if Probe(os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "p5-shell-helper" {
		if ServeHelper(os.Stdin, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}
func fixture(t *testing.T) (*Coordinator, ownershell.Binding, ownershell.SignedGrant, ed25519.PrivateKey) {
	t.Helper()
	cwd := t.TempDir()
	i, err := ResolveIdentity(cwd)
	if err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err = unix.Stat(cwd, &st); err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b := ownershell.Binding{RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: st.Dev, Inode: st.Ino}, ManifestDigest: "digest", Session: "session", Owner: "owner", Launch: "launch", Connection: "connection", Audience: ownershell.RuntimeAudience("connection", 1), Runtime: "host", Live: true, Consent: "consent", HostConsentValid: true}
	// A joined helper must exit on the production deadline. Disable only the
	// race runtime's artificial one-second exit sleep; detection remains enabled.
	fresh := true
	command := Command{Recovery: func(context.Context, protocol.SupervisorLaunch) error {
		if fresh {
			fresh = false
			return nil
		}
		return ErrClosed
	}, Executable: "/usr/bin/env", Args: []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestRuntimeHelperProcess$", "--", "p5-shell-helper"}, Identity: i, Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, Consent: &protocol.HostShellConsent{ID: "consent", OwnerID: b.Owner, LaunchGeneration: b.Launch, MachineID: i.Machine, Account: i.Account, UID: i.UID, Shell: i.Shell, Cwd: i.Cwd, ChallengeID: "challenge"}}
	c, err := NewCoordinator(new(sync.Mutex), b, command, pub)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	g := ownershell.SignGrant(key, protocol.ShellGrant{Version: 1, Audience: b.Audience, OwnerID: b.Owner, LoginID: "login", SessionID: b.Session, LaunchGeneration: b.Launch, ConnectionID: b.Connection, ShellGeneration: "shell", Runtime: b.Runtime, ConsentID: b.Consent, PolicyRevision: 1, IssuedAt: now, ExpiresAt: now + 30})
	if _, err = c.RecoverLaunch(t.Context(), c.launch()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_, _ = c.CloseLaunch(ctx, c.launch(), "test_cleanup")
	})
	return c, b, g, key
}
func waitOutput(t *testing.T, out <-chan []byte, marker string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var data string
	for {
		select {
		case chunk, ok := <-out:
			if !ok {
				t.Fatal("runtime output closed before marker")
			}
			data += string(chunk)
			if strings.Contains(data, marker) {
				return
			}
		case <-ctx.Done():
			t.Fatal("runtime did not produce expected marker")
		}
	}
}
func TestHostPTYAndExactCleanup(t *testing.T) {
	c, b, g, key := fixture(t)
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Input(t.Context(), g, 1, []byte("printf '\\125\\116\\111\\121\\125\\105'\n")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "UNIQUE")
	if err = c.Resize(t.Context(), g, 1, 120, 40); err != nil {
		t.Fatal(err)
	}
	proof, err := c.CloseShell(t.Context(), ownershell.SupervisorIdentity(b, g), "stop")
	if err != nil {
		t.Fatal(err)
	}
	if err = proof.ValidateFor(c.launch(), g.Grant.ShellGeneration); err != nil {
		t.Fatal(err)
	}
	if err = c.Input(t.Context(), g, 2, []byte("stale")); err == nil {
		t.Fatal("closed shell accepted input")
	}
}
func TestHostDetachedDescendantCannotWriteAfterAck(t *testing.T) {
	c, b, g, key := fixture(t)
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(c.command.Identity.Cwd, "descendant.pid")
	// setsid detaches a real child and closes all PTY descriptors. Tracking must
	// still follow the fork tree rather than process group or PTY EOF.
	command := fmt.Sprintf("setsid /bin/sh -c 'echo $$ > %s; while :; do sleep 1; done' </dev/null >/dev/null 2>&1 &\nprintf '\\122\\105\\101\\104\\131'\n", pidFile)
	if err = c.Input(t.Context(), g, 1, []byte(command)); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "READY")
	var pid int
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		raw, e := os.ReadFile(pidFile)
		if e == nil {
			_, _ = fmt.Sscanf(string(raw), "%d", &pid)
			if pid > 0 {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("detached child not started")
		case <-time.After(time.Millisecond):
		}
	}
	proof, err := c.CloseLaunch(ctx, c.launch(), "disable")
	if err != nil {
		t.Fatal(err)
	}
	if err = proof.ValidateFor(c.launch(), ""); err != nil {
		t.Fatal(err)
	}
	if err = unix.Kill(pid, 0); err == nil {
		t.Fatal("detached descendant survived proof")
	}
}
func TestHostOfflineExpiryWithBlockedConsumer(t *testing.T) {
	c, b, g, key := fixture(t)
	g.Grant.ExpiresAt = time.Now().Unix() + 2
	g = ownershell.SignGrant(key, g.Grant)
	_, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	c.sessionMu.Lock()
	peer := c.peers[g.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	// No output consumption or server renewal participates in the helper timer.
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	select {
	case <-peer.done:
	case <-deadline.C:
		t.Fatal("offline runtime exceeded lease bound")
	}
	if err = c.Renew(t.Context(), g); err == nil {
		t.Fatal("expired shell resurrected")
	}
	proof, err := c.RecoverLaunch(t.Context(), c.launch())
	if err != nil {
		t.Fatal(err)
	}
	if err = proof.ValidateFor(c.launch(), ""); err != nil {
		t.Fatal(err)
	}
}
func TestConsentSpoofAndPausedAdmission(t *testing.T) {
	c, b, g, key := fixture(t)
	command := c.command
	copyConsent := *command.Consent
	command.Consent = &copyConsent
	command.Consent.UID++
	if _, err := NewCoordinator(new(sync.Mutex), b, command, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("spoofed host UID accepted")
	}
	if err := c.Notify(t.Context(), c.launch(), "paused"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("paused runtime exec")
	}
}

func TestRealHostOutputFloodTerminatesBoundedly(t *testing.T) {
	c, b, g, key := fixture(t)
	_, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	c.sessionMu.Lock()
	peer := c.peers[g.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	if err = c.Input(t.Context(), g, 1, []byte("yes\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	select {
	case <-peer.done:
	case <-ctx.Done():
		t.Fatal("output flood held supervisor or reaper")
	}
}
func TestRealHostSupervisorCrashRetainsRecoveryFence(t *testing.T) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) }()
	c, b, g, key := fixture(t)
	_, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	c.sessionMu.Lock()
	peer := c.peers[g.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	pidPath := filepath.Join(c.command.Identity.Cwd, "root.pid")
	if err = c.Input(t.Context(), g, 1, []byte("echo $$ > "+pidPath+"\n")); err != nil {
		t.Fatal(err)
	}
	var rootPID int
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		if data, e := os.ReadFile(pidPath); e == nil {
			_, _ = fmt.Sscanf(string(data), "%d", &rootPID)
			if rootPID > 0 {
				break
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("owned shell PID not reported")
		case <-time.After(time.Millisecond):
		}
	}
	defer func() { var status unix.WaitStatus; _, _ = unix.Wait4(rootPID, &status, 0, nil) }()
	// This is exactly the helper process started by this test, never a name scan.
	if err = peer.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
	case <-time.After(3 * time.Second):
		t.Fatal("owned supervisor did not exit")
	}
	if _, err = c.CloseLaunch(t.Context(), c.launch(), "crash"); err == nil {
		t.Fatal("helper crash mistaken for reaping proof")
	}
	if _, err = c.RecoverLaunch(t.Context(), c.launch()); err == nil {
		t.Fatal("empty process map bypassed orphan reconciliation")
	}
	g.Grant.ShellGeneration = "replacement"
	g = ownershell.SignGrant(key, g.Grant)
	if _, err = c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("uncertain cleanup reopened")
	}
}
func TestSyncHeaderBeforeFreezeAndNoPausedExec(t *testing.T) {
	c, b, g, key := fixture(t)
	_, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	signaled := false
	c.Signal = func(phase protocol.SupervisorPhase) error {
		if phase == "syncing" {
			signaled = true
		}
		return nil
	}
	if err = c.Notify(t.Context(), c.launch(), "syncing"); err != nil || !signaled {
		t.Fatal("freeze acknowledged before header signal")
	}
	if err = c.Input(t.Context(), g, 1, []byte("blocked")); err == nil {
		t.Fatal("syncing runtime executed input")
	}
	if err = c.Renew(t.Context(), g); err != nil {
		t.Fatal("bounded sync could not retain pending grant")
	}
	if err = c.Notify(t.Context(), c.launch(), "running"); err != nil {
		t.Fatal(err)
	}
	if err = c.Notify(t.Context(), c.launch(), "paused"); err != nil {
		t.Fatal(err)
	}
	c.sessionMu.Lock()
	remaining := len(c.peers)
	c.sessionMu.Unlock()
	if remaining != 0 {
		t.Fatal("paused shell not closed")
	}
	if err = c.Notify(t.Context(), c.launch(), "running"); err != nil {
		t.Fatal(err)
	}
	g.Grant.ShellGeneration = "new"
	g = ownershell.SignGrant(key, g.Grant)
	if _, err = c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("running notification cleared recovery fence")
	}
}

func TestCanceledCloseStillReapsAndRequiresRecoveryProof(t *testing.T) {
	c, b, g, key := fixture(t)
	_, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	c.sessionMu.Lock()
	peer := c.peers[g.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = c.CloseShell(canceled, ownershell.SupervisorIdentity(b, g), "canceled"); err == nil {
		t.Fatal("cancellation treated as proof")
	}
	select {
	case <-peer.done:
	case <-time.After(4 * time.Second):
		t.Fatal("canceled caller abandoned cleanup")
	}
	next := g
	next.Grant.ShellGeneration = "next"
	next = ownershell.SignGrant(key, next.Grant)
	if _, err = c.Open(t.Context(), b, next, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("uncertain teardown reopened before recovery")
	}
	if _, err = c.RecoverLaunch(t.Context(), c.launch()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Open(t.Context(), b, next, key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal("verified recovery refused new shell", err)
	}
}
func TestConcurrentSyncCannotBeUnfencedByOlderRecovery(t *testing.T) {
	c, _, g, _ := fixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	c.sessionMu.Lock()
	c.recovered = false
	c.command.Recovery = func(ctx context.Context, _ protocol.SupervisorLaunch) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.sessionMu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	result := make(chan error, 1)
	go func() { _, err := c.RecoverLaunch(ctx, c.launch()); result <- err }()
	<-entered
	if err := c.AdmitShell(ctx, ownershell.SupervisorIdentity(c.binding, g)); err == nil {
		t.Fatal("admitted during orphan recovery")
	}
	if err := c.Notify(ctx, c.launch(), "syncing"); err != nil {
		t.Fatal(err)
	}
	if err := c.Notify(ctx, c.launch(), "running"); err != nil {
		t.Fatal(err)
	}
	finish()
	if err := <-result; err == nil {
		t.Fatal("older recovery cleared a newer lifecycle fence")
	}
	if err := c.AdmitShell(t.Context(), ownershell.SupervisorIdentity(c.binding, g)); err == nil {
		t.Fatal("newer lifecycle fence was bypassed")
	}
}

func TestKernelRejectsUntracedCloneEscape(t *testing.T) {
	c, b, g, key := fixture(t)
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	// The child exits immediately and its Python parent reaps it even in the
	// negative control, so testing the bypass never leaves an escaped writer.
	script := "import ctypes,os,platform; l=ctypes.CDLL(None,use_errno=True); n=56 if platform.machine()=='x86_64' else 220; p=l.syscall(n,17|0x800000,0,0,0,0); e=ctypes.get_errno(); (os._exit(0) if p==0 else None); (os.waitpid(p,0) if p>0 else None); print('FILTER_'+'RESULT_'+('DENIED' if p<0 and e==1 else 'ESCAPED'),flush=True)"
	command := "python3 -c \"" + script + "\"\n"
	if err = c.Input(t.Context(), g, 1, []byte(command)); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "FILTER_RESULT_DENIED")
}

func TestMemberShellStaysAnchoredToPreparedPublicRoot(t *testing.T) {
	base, b, g, key := fixture(t)
	root := base.command.Identity.Cwd
	member := filepath.Join(root, "member")
	if err := os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := ResolveIdentity(member)
	if err != nil {
		t.Fatal(err)
	}
	command := base.command
	command.Identity = identity
	command.PublicRoot = root
	consent := *command.Consent
	consent.Cwd = member
	command.Consent = &consent
	command.Recovery = func(context.Context, protocol.SupervisorLaunch) error { return nil }
	c, err := NewCoordinator(new(sync.Mutex), b, command, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.RecoverLaunch(t.Context(), c.launch()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_, _ = c.CloseLaunch(ctx, c.launch(), "test_cleanup")
	})
	stream, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Input(t.Context(), g, 1, []byte("printf '\\115\\105\\115\\102\\105\\122'\n")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, stream, "MEMBER")
	if _, err = c.CloseLaunch(t.Context(), c.launch(), "replace_member"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RecoverLaunch(t.Context(), c.launch()); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(member, member+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(t.TempDir(), member); err != nil {
		t.Fatal(err)
	}
	g.Grant.ShellGeneration = "replacement-member"
	g = ownershell.SignGrant(key, g.Grant)
	if _, err = c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("member cwd escaped prepared public root")
	}
}

func TestStalledPTYWriteCannotHoldCleanupProof(t *testing.T) {
	c, b, g, key := fixture(t)
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Input(t.Context(), g, 1, []byte("stty raw -echo; printf '\\102\\114\\117\\103\\113'; sleep 30\n")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "BLOCK")
	c.sessionMu.Lock()
	peer := c.peers[g.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	// The slave remains open in a sleeping foreground child and stops reading.
	// Exercise the helper directly so session rate limiting cannot masquerade as
	// a PTY deadline. One write can fit in the kernel's input queue.
	blocked := false
	for seq := uint64(2); seq < 10; seq++ {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		err = peer.call(ctx, HelperFrame{Op: "input", Grant: g, Seq: seq, Data: []byte(strings.Repeat("x", 16384))})
		cancel()
		if err != nil {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("stalled raw PTY accepted an unbounded write")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if _, err = c.CloseLaunch(ctx, c.launch(), "stalled_input"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
	case <-ctx.Done():
		t.Fatal("in-flight PTY writer survived cleanup proof")
	}
}

func TestRealHostEnvironmentExcludesProviderAndBridgeSecrets(t *testing.T) {
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "BLERG_RUNNER_SESSION_TOKEN", "BLERG_MCP_BRIDGE_TOKEN", "SSH_AUTH_SOCK", "LD_PRELOAD", "SHELL"} {
		t.Setenv(name, "p5-parent-only-sentinel")
	}
	c, b, g, key := fixture(t)
	out, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	// No secret value is sent as input. The shell checks its own inherited state.
	command := "if [ -n \"${ANTHROPIC_API_KEY-}${OPENAI_API_KEY-}${BLERG_RUNNER_SESSION_TOKEN-}${BLERG_MCP_BRIDGE_TOKEN-}${SSH_AUTH_SOCK-}${LD_PRELOAD-}\" ]; then printf BAD_ENV; else printf '\\105\\116\\126\\137\\103\\114\\105\\101\\116'; fi\n"
	if err = c.Input(t.Context(), g, 1, []byte(command)); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "ENV_CLEAN")
	if _, err = c.CloseLaunch(t.Context(), c.launch(), "environment_gate"); err != nil {
		t.Fatal(err)
	}
}
