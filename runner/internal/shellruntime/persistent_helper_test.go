//go:build linux

package shellruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestPersistentHelperProcess(t *testing.T) {
	if os.Args[len(os.Args)-1] != "p5-persistent-helper" {
		return
	}
	// Test-only elapsed source. Production always uses its verified platform clock.
	boot := time.Now()
	clock := func() (time.Time, time.Duration, error) { return time.Now(), time.Since(boot), nil }
	resolve := func(cwd string) (Identity, error) {
		raw, _ := os.ReadFile(filepath.Join(cwd, "identity-state"))
		if string(raw) == "unknown" {
			return Identity{}, ownershell.ErrUnknown
		}
		return ResolvePersistentIdentity(cwd)
	}
	if serveHelperWithResolvers(os.Stdin, os.Stdout, ResolveIdentity, resolve, time.Now, func() (ownershell.LeaseClock, error) { return clock, nil }) != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func persistentPeerFixture(t *testing.T, lifetime time.Duration, configure ...func(*Command)) (*helperPeer, ownershell.SignedGrant, ed25519.PrivateKey, *atomic.Int32) {
	t.Helper()
	base, b, old, key := fixture(t)
	now := time.Now().Unix()
	identity, identityErr := ResolvePersistentIdentity(base.command.Identity.Cwd)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	base.command.Identity = identity
	custody := protocol.ShellCustody{Version: 2, Owner: b.Owner, Login: "login", Session: b.Session, Launch: b.Launch, Runtime: b.Runtime, RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner", Daemon: "daemon", Shell: "shell", Nonce: "custody-nonce", Identity: RuntimeIdentityDigest(base.command.Identity), Consent: "consent", Created: now, LifetimeUntil: now + int64(lifetime/time.Second)}
	g := old.Grant
	g.Version = 2
	g.CustodyID = custody.Digest()
	g.AccessEpoch = 1
	g.ExpiresAt = now + 2
	command := base.command
	command.Custody = &custody
	command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestPersistentHelperProcess$", "--", "p5-persistent-helper"}
	dirty := new(atomic.Int32)
	command.identityInvalidated = func() { dirty.Add(1) }
	for _, option := range configure {
		option(&command)
	}
	g.Privileged = command.Custody.Privileged
	g.CustodyID = command.Custody.Digest()
	signed := ownershell.SignGrant(key, g)
	peer, err := startPeer(t.Context(), command, signed, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = peer.call(ctx, HelperFrame{Op: "close", Grant: signed})
	})
	return peer, signed, key, dirty
}

func TestPersistentHelperSurvivesLapsedGrantAndAdopts(t *testing.T) {
	peer, g, key, _ := persistentPeerFixture(t, time.Minute)
	time.Sleep(2500 * time.Millisecond)
	if err := peer.call(t.Context(), HelperFrame{Op: "inspect"}); err != nil {
		t.Fatal(err)
	}
	if peer.inspectedActive.Load() || peer.inspectedEpoch.Load() != g.Grant.AccessEpoch {
		t.Fatal("inspection hid a lapsed access epoch")
	}
	select {
	case <-peer.done:
		t.Fatal("lapsed access killed helper")
	default:
	}
	if peer.call(t.Context(), HelperFrame{Op: "input", Grant: g, Seq: 1, Data: []byte("stale\n")}) == nil {
		t.Fatal("lapsed input accepted")
	}
	next := g.Grant
	next.AccessEpoch++
	next.ConnectionID = "new-connection"
	next.Audience = "new-audience"
	next.IssuedAt = time.Now().Unix()
	next.ExpiresAt = next.IssuedAt + 30
	renewed := ownershell.SignGrant(key, next)
	if err := peer.call(t.Context(), HelperFrame{Op: "prepare", Transaction: "adopt", Key: key.Public().(ed25519.PublicKey), Grant: renewed}); err != nil {
		t.Fatal(err)
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "commit", Transaction: "adopt"}); err != nil {
		t.Fatal(err)
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "input", Grant: renewed, Seq: 1, Data: []byte("printf 'PERSISTENT_MARKER\\n'\n")}); err != nil {
		t.Fatal(err)
	}
	chunks := make(chan []byte, 8)
	go func() {
		defer close(chunks)
		offset := uint64(0)
		for {
			frame, err := peer.readRetained(t.Context(), renewed.Grant.AccessEpoch, offset)
			if err != nil {
				return
			}
			select {
			case chunks <- frame.Data:
				offset = frame.Offset + uint64(len(frame.Data))
			case <-peer.done:
				return
			}
		}
	}()
	waitOutput(t, chunks, "PERSISTENT_MARKER")
}

func TestPersistentHelperNaturalExitDoesNotInvalidateSiblings(t *testing.T) {
	peer, g, key, dirty := persistentPeerFixture(t, time.Minute)
	// Re-establish a full access window so a slow race runtime cannot expire it.
	next := g.Grant
	next.AccessEpoch++
	next.IssuedAt = time.Now().Unix()
	next.ExpiresAt = next.IssuedAt + 30
	fresh := ownershell.SignGrant(key, next)
	if err := peer.call(t.Context(), HelperFrame{Op: "prepare", Transaction: "exit", Key: key.Public().(ed25519.PublicKey), Grant: fresh}); err != nil {
		t.Fatal(err)
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "commit", Transaction: "exit"}); err != nil {
		t.Fatal(err)
	}
	_ = peer.call(t.Context(), HelperFrame{Op: "input", Grant: fresh, Seq: 1, Data: []byte("exit\n")})
	select {
	case <-peer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("natural exit not reaped")
	}
	peer.mu.Lock()
	clean := peer.clean
	peer.mu.Unlock()
	if !clean {
		t.Fatal("natural exit has no physical cleanup acknowledgment")
	}
	if dirty.Load() != 0 {
		t.Fatal("natural exit invalidated sibling consent")
	}
}

func TestPersistentHelperInvalidFramesCannotKillCustody(t *testing.T) {
	peer, g, _, _ := persistentPeerFixture(t, time.Minute)
	invalid := g
	invalid.Signature = "forged"
	if peer.call(t.Context(), HelperFrame{Op: "input", Grant: invalid, Seq: 1, Data: []byte("exit\n")}) == nil {
		t.Fatal("forged input accepted")
	}
	if peer.call(t.Context(), HelperFrame{Op: "input", Grant: g, Seq: 2, Data: []byte("exit\n")}) == nil {
		t.Fatal("wrong sequence accepted")
	}
	raw, err := json.Marshal(HelperFrame{Op: "close", ID: 100000, Cleanup: cleanupRequest{Custody: g.Grant.CustodyID, Helper: "other", Transaction: "forged", MAC: "forged"}})
	if err != nil {
		t.Fatal(err)
	}
	peer.writeMu.Lock()
	_, err = peer.input.Write(append(raw, '\n'))
	peer.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = peer.call(t.Context(), HelperFrame{Op: "input", Grant: g, Seq: 1, Data: []byte("printf 'STILL_ALIVE\\n'\n")}); err != nil {
		t.Fatal("invalid frame killed or consumed sequence", err)
	}
	select {
	case <-peer.done:
		t.Fatal("invalid frame ended custody")
	default:
	}
}

func TestPersistentHelperLifetimeRunsWithNoOutputConsumer(t *testing.T) {
	peer, g, _, dirty := persistentPeerFixture(t, 2*time.Second)
	if err := peer.call(t.Context(), HelperFrame{Op: "input", Grant: g, Seq: 1, Data: []byte("while :; do printf 'bounded output\\n'; done\n")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("output blocked lifetime cleanup")
	}
	peer.mu.Lock()
	clean := peer.clean
	peer.mu.Unlock()
	if !clean || dirty.Load() != 0 {
		t.Fatal("lifetime end lost exact per-shell cleanup")
	}
}

func TestPersistentHelperCrashDoesNotClaimIdentityChanged(t *testing.T) {
	uncertain := new(atomic.Int32)
	peer, _, _, dirty := persistentPeerFixture(t, time.Minute, func(command *Command) { command.cleanupUncertain = func() { uncertain.Add(1) } })
	if err := peer.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("crashed helper not joined")
	}
	if dirty.Load() != 0 {
		t.Fatal("helper loss was reported as an account identity change")
	}
	peer.mu.Lock()
	failure, clean := peer.failure, peer.clean
	peer.mu.Unlock()
	if failure == nil || !clean || uncertain.Load() != 1 || peer.proveTreeDead == nil {
		t.Fatal("helper crash did not settle through independent tree-death proof")
	}
}

func TestPersistentHelperReplaysEvictionAfterLapsedAccess(t *testing.T) {
	peer, g, key, _ := persistentPeerFixture(t, time.Minute)
	// The shell produces this after its two-second access grant has lapsed.
	if err := peer.call(t.Context(), HelperFrame{Op: "input", Grant: g, Seq: 1, Data: []byte("sleep 3; printf '%400000s' X; printf 'OFFLINE_END\\n'\n")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	select {
	case <-peer.done:
		t.Fatal("offline output ended custody")
	default:
	}
	next := g.Grant
	next.AccessEpoch++
	next.IssuedAt = time.Now().Unix()
	next.ExpiresAt = next.IssuedAt + 30
	fresh := ownershell.SignGrant(key, next)
	if err := peer.call(t.Context(), HelperFrame{Op: "prepare", Transaction: "large-replay", Key: key.Public().(ed25519.PublicKey), Grant: fresh}); err != nil {
		t.Fatal(err)
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "commit", Transaction: "large-replay"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var offset uint64
	var gap bool
	var suffix []byte
	for {
		f, err := peer.readRetained(ctx, next.AccessEpoch, offset)
		if err != nil {
			e, ack := peer.stream.acknowledgment()
			local, _ := peer.stream.history.Read(offset, ownershell.MaxFrame)
			t.Fatalf("retained output did not drain: error=%v offset=%d local_end=%d gap=%v ack_epoch=%d ack=%d", err, offset, local.End, gap, e, ack)
		}
		gap = gap || f.Gap
		if f.Offset != offset && !f.Gap {
			t.Fatal("unexplained output hole")
		}
		offset = f.Offset + uint64(len(f.Data))
		suffix = append(suffix, f.Data...)
		marker := bytes.Contains(suffix, []byte("OFFLINE_END\r\n"))
		if len(suffix) > 64 {
			suffix = suffix[len(suffix)-64:]
		}
		if marker {
			break
		}
	}
	if !gap || offset < 400000 {
		t.Fatal("eviction was hidden or positions were renumbered", gap, offset)
	}
}

func TestPersistentHelperSyncWatchdogStartsWithExpiredAccess(t *testing.T) {
	peer, _, _, dirty := persistentPeerFixture(t, time.Minute)
	time.Sleep(2500 * time.Millisecond)
	until := time.Now().Add(3 * time.Second).Unix()
	if err := peer.call(t.Context(), HelperFrame{Op: "freeze", SyncUntil: until}); err != nil {
		t.Fatal("independent sync could not start after access expired", err)
	}
	select {
	case <-peer.done:
		t.Fatal("expired access killed frozen custody")
	case <-time.After(500 * time.Millisecond):
	}
	select {
	case <-peer.done:
	case <-time.After(4 * time.Second):
		t.Fatal("independent sync watchdog did not clean up frozen shell")
	}
	if dirty.Load() != 0 {
		t.Fatal("sync timeout revoked sibling consent")
	}
	peer.mu.Lock()
	clean := peer.clean
	peer.mu.Unlock()
	if !clean {
		t.Fatal("sync timeout has no physical cleanup proof")
	}
}

func TestPersistentHelperCapacityCreditIsNotServerReceipt(t *testing.T) {
	peer, g, key, _ := persistentPeerFixture(t, time.Minute)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	_, err := peer.readRetained(ctx, g.Grant.AccessEpoch, 0)
	cancel()
	if err != nil {
		t.Fatal("no initial helper output", err)
	}
	// The daemon returns capacity credit, but no server retention receipt exists.
	time.Sleep(2500 * time.Millisecond)
	next := g.Grant
	next.AccessEpoch++
	next.IssuedAt = time.Now().Unix()
	next.ExpiresAt = next.IssuedAt + 30
	fresh := ownershell.SignGrant(key, next)
	if err := peer.call(t.Context(), HelperFrame{Op: "prepare", Transaction: "unreceived-server-replay", Key: key.Public().(ed25519.PublicKey), Grant: fresh}); err != nil {
		t.Fatal(err)
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "commit", Transaction: "unreceived-server-replay"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		epoch, offset := peer.stream.acknowledgment()
		if epoch == next.AccessEpoch && offset > 0 {
			if err := peer.acknowledgeRetained(t.Context(), epoch, offset); err != nil {
				t.Fatal("server retention receipt refused", err)
			}
			if err := peer.acknowledgeRetained(t.Context(), g.Grant.AccessEpoch, offset); err == nil {
				t.Fatal("stale server receipt accepted")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("capacity credit advanced the server receipt and suppressed replay")
}

func TestPersistentHelperNaturalExitDrainsFinalPrivateHistory(t *testing.T) {
	peer, g, key, _ := persistentPeerFixture(t, time.Minute)
	next := g.Grant
	next.AccessEpoch++
	next.IssuedAt = time.Now().Unix()
	next.ExpiresAt = next.IssuedAt + 30
	fresh := ownershell.SignGrant(key, next)
	if err := peer.call(t.Context(), HelperFrame{Op: "prepare", Transaction: "final-output", Key: key.Public().(ed25519.PublicKey), Grant: fresh}); err != nil {
		t.Fatal(err)
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "commit", Transaction: "final-output"}); err != nil {
		t.Fatal(err)
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "input", Grant: fresh, Seq: 1, Data: []byte("printf '%400000s' X; printf 'FINAL_PRIVATE_OUTPUT\\n'; exit\n")}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var offset uint64
	var suffix []byte
	for {
		f, err := peer.readRetained(ctx, next.AccessEpoch, offset)
		if err != nil {
			t.Fatal("natural exit lost final private output", err)
		}
		if f.Offset != offset && !f.Gap {
			t.Fatal("unexplained final output hole")
		}
		offset = f.Offset + uint64(len(f.Data))
		suffix = append(suffix, f.Data...)
		if bytes.Contains(suffix, []byte("FINAL_PRIVATE_OUTPUT\r\n")) && offset >= 400000 {
			break
		}
		if len(suffix) > 64 {
			suffix = suffix[len(suffix)-64:]
		}
	}
	select {
	case <-peer.done:
	case <-ctx.Done():
		t.Fatal("final history drained but helper did not finish")
	}
}

func TestPersistentHelperKnownRootLossClosesBeforeResolverUnknown(t *testing.T) {
	var root string
	peer, _, _, dirty := persistentPeerFixture(t, time.Minute, func(c *Command) { root = c.Identity.Cwd })
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Rename(moved, root); err != nil {
			t.Error("restore private test directory", err)
		}
	}()
	// The missing member also makes account/cwd resolution unavailable. That
	// unknown observation cannot hide the independent known root transition.
	select {
	case <-peer.done:
	case <-time.After(4 * time.Second):
		t.Fatal("known missing root was treated only as resolver outage")
	}
	if dirty.Load() != 1 {
		t.Fatal("known identity transition was not latched")
	}
}

// Every access operation verifies identity, and credit frames arrive in bursts. However many find
// the root gone, the daemon hears of it once.
func TestPersistentHelperReportsIdentityTransitionOnce(t *testing.T) {
	var root string
	peer, _, _, dirty := persistentPeerFixture(t, time.Minute, func(c *Command) { root = c.Identity.Cwd })
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Rename(moved, root); err != nil {
			t.Error("restore private test directory", err)
		}
	}()
	for range 8 {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		_ = peer.call(ctx, HelperFrame{Op: "inspect"})
		cancel()
	}
	select {
	case <-peer.done:
	case <-time.After(4 * time.Second):
		t.Fatal("known missing root did not end the helper")
	}
	if n := dirty.Load(); n != 1 {
		t.Fatalf("identity transition reported %d times", n)
	}
}

func TestPersistentHelperRequiresFreshIdentityForAdoption(t *testing.T) {
	var cwd string
	peer, g, key, _ := persistentPeerFixture(t, time.Minute, func(c *Command) { cwd = c.Identity.Cwd })
	state := filepath.Join(cwd, "identity-state")
	if err := os.WriteFile(state, []byte("unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	next := g.Grant
	next.AccessEpoch = 2
	next.IssuedAt = time.Now().Unix()
	next.ExpiresAt = next.IssuedAt + 30
	signed := ownershell.SignGrant(key, next)
	if peer.call(t.Context(), HelperFrame{Op: "prepare", Transaction: "unverified", Key: key.Public().(ed25519.PublicKey), Grant: signed}) == nil {
		t.Fatal("identity outage authorized adoption")
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	next.AccessEpoch = 3
	signed = ownershell.SignGrant(key, next)
	if err := peer.call(t.Context(), HelperFrame{Op: "prepare", Transaction: "verified", Key: key.Public().(ed25519.PublicKey), Grant: signed}); err != nil {
		t.Fatal(err)
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "commit", Transaction: "verified"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
		t.Fatal("identity outage killed custody")
	default:
	}
}

func TestPersistentHelperRejectsUnauthenticatedLifecycleControl(t *testing.T) {
	peer, g, _, _ := persistentPeerFixture(t, time.Minute)
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	next := g.Grant
	next.AccessEpoch++
	next.ExpiresAt = time.Now().Add(30 * time.Second).Unix()
	for _, op := range []string{"prepare", "commit", "suspend", "freeze", "thaw"} {
		f := HelperFrame{Op: op, ID: peer.seq.Add(1), Epoch: g.Grant.AccessEpoch, Transaction: "attacker", Key: pub, Grant: ownershell.SignGrant(key, next), SyncUntil: time.Now().Add(time.Minute).Unix()}
		reply := make(chan HelperFrame, 1)
		peer.mu.Lock()
		peer.pending[f.ID] = reply
		peer.mu.Unlock()
		raw, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		peer.writeMu.Lock()
		_, err = peer.input.Write(append(raw, '\n'))
		peer.writeMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-reply:
			if result.OK {
				t.Fatalf("unauthenticated %s accepted", op)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not answer", op)
		}
	}
}

func TestPersistentHelperPrivateTransportAndContainment(t *testing.T) {
	peer, _, _, _ := persistentPeerFixture(t, time.Minute)
	// The helper is already tracing its PTY here. Neither process endpoint may
	// expose bootstrap authority through proc descriptors, even to the same UID.
	for _, pid := range []int{peer.cmd.Process.Pid, os.Getpid()} {
		path := fmt.Sprintf("/proc/%d/fd/%d", pid, 0)
		if pid == os.Getpid() {
			path = fmt.Sprintf("/proc/self/fd/%d", peer.input.Fd())
		}
		fd, err := os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			_ = fd.Close()
			t.Fatal("private helper socket was reopened")
		}
	}
	// A parent with the same UID could normally read a dumpable child's memory.
	// This check independently verifies the helper's process protection, rather
	// than relying only on socket descriptors being non-reopenable.
	if mem, err := os.OpenFile(fmt.Sprintf("/proc/%d/mem", peer.cmd.Process.Pid), os.O_RDONLY, 0); err == nil {
		_ = mem.Close()
		t.Fatal("helper remains dumpable")
	}
	value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || value != 0 {
		t.Fatal("daemon endpoint remains dumpable", value, err)
	}
}
