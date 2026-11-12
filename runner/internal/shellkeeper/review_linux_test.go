//go:build linux

package shellkeeper

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func clientHello(t *testing.T, epoch Epoch) message {
	t.Helper()
	secret, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	build, err := BuildID()
	if err != nil {
		t.Fatal(err)
	}
	return message{Proof: transcript{Version: Version, Epoch: epoch, ClientNonce: strings.Repeat("a", 64), ClientPublic: secret.PublicKey().Bytes(), ClientBuild: build}}
}

func TestRoleSeparationAndSignedJumpCannotWedge(t *testing.T) {
	dir, _, epoch := keeperFixture(t)
	k, err := loadKeeper(dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := loadDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Daemon) != 0 || len(d.Keeper) != 0 {
		t.Fatal("role loaded peer private key")
	}
	for _, role := range []string{"daemon-key.json", "keeper-key.json"} {
		data, err := os.ReadFile(filepath.Join(dir, role))
		if err != nil {
			t.Fatal(err)
		}
		var key roleKey
		if json.Unmarshal(data, &key) != nil || len(key.Key) != 64 || len(key.Peer) != 32 {
			t.Fatal("role file")
		}
	}
	jump := epoch
	jump.Number = ^uint64(0)
	jump.Signature = sign(d.Daemon, "epoch", epochBody(jump))
	if _, err := Inspect(t.Context(), dir, jump); err == nil {
		t.Fatal("signed jump accepted")
	}
	if err := RequireEmpty(t.Context(), dir, epoch); err != nil {
		t.Fatal("forged jump wedged real daemon", err)
	}
	if follows(epoch, jump) {
		t.Fatal("epoch step unbounded")
	}
}

func TestLockReplacementAfterFlockRefused(t *testing.T) {
	dir := provision(t)
	f, err := acquireLockAfter(dir, "fixture.lock", func(_ *os.File) {
		path := filepath.Join(dir, "fixture.lock")
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil {
		_ = f.Close()
		t.Fatal("replaced lock accepted")
	}
}

func TestStaleCrashSocketModeAndWrongType(t *testing.T) {
	for _, kind := range []string{"wide_socket", "regular", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := provision(t)
			path := filepath.Join(dir, "keeper.sock")
			switch kind {
			case "wide_socket":
				l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				l.SetUnlinkOnClose(false)
				_ = l.Close()
				if err = os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			case "regular":
				if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(dir, "keeper-key.json"), path); err != nil {
					t.Fatal(err)
				}
			}
			s, err := Listen(t.Context(), dir)
			if s != nil {
				_ = s.Close()
			}
			if (err == nil) != (kind == "wide_socket") {
				t.Fatal("stale socket policy", err)
			}
			if kind == "regular" {
				data, _ := os.ReadFile(path)
				if string(data) != "preserve" {
					t.Fatal("non-socket removed")
				}
			}
			if kind == "symlink" {
				if _, err := os.Readlink(path); err != nil {
					t.Fatal("symlink removed")
				}
			}
		})
	}
}

func TestServerDeadlineClosesSilentPeer(t *testing.T) {
	dir, _, _ := keeperFixture(t)
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "keeper.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [1]byte
	_, err = c.Read(b[:])
	if !errors.Is(err, io.EOF) {
		t.Fatal("server did not expire silent pre-auth peer", err)
	}
}

func TestDescriptorExhaustionRetriesInsteadOfStopping(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &Server{stop: cancel}
	var delays []time.Duration
	calls := 0
	s.acceptConnections(ctx, func() (*net.UnixConn, error) {
		calls++
		switch calls {
		case 1:
			return nil, unix.EMFILE
		case 2:
			return nil, unix.ENFILE
		case 3:
			return nil, unix.ECONNABORTED
		case 4:
			return nil, errors.New("listener fault")
		default:
			return nil, net.ErrClosed
		}
	}, func(_ context.Context, d time.Duration) bool { delays = append(delays, d); return true }, s.hello)
	if calls != 5 || len(delays) != 4 || delays[0] <= 0 || delays[1] <= delays[0] {
		t.Fatal("exhaustion stopped server or busy-spun", calls, delays)
	}
}

func TestInitialProvisionCrashWindowsNeverPublishPartialState(t *testing.T) {
	for _, stage := range []string{"stage", "daemon_key", "keeper_key", "epoch", "published"} {
		t.Run(stage, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "keeper")
			cmd := exec.CommandContext(t.Context(), os.Args[0], "initialize-crash", dir, stage)
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 91 {
				t.Fatal("crash checkpoint not reached", err)
			}
			if stage != "published" {
				if _, err := os.Lstat(dir); !os.IsNotExist(err) {
					t.Fatal("partial target published")
				}
			}
			if err := Initialize(dir); err != nil {
				t.Fatal("crash left an installation wedge", err)
			}
			if _, err := LoadIdentity(dir); err != nil {
				t.Fatal(err)
			}
			if _, err := loadEpoch(dir); err != nil {
				t.Fatal(err)
			}
		})
	}
	dir := provision(t)
	if err := writePrivate(dir, "epoch.json", Epoch{}, false); err == nil {
		t.Fatal("initial publication overwrote existing file")
	}
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(dir, "epoch.json"), &st); err != nil || st.Nlink != 1 {
		t.Fatal("state publication retained a hardlink", err)
	}
	if err := os.Remove(filepath.Join(dir, "epoch.json")); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(dir); err == nil || !strings.Contains(err.Error(), RepairInstructions) {
		t.Fatal("refusal lacks repair instructions", err)
	}
}

func TestStateFilePublicationHasNoCrashHardlinkWindow(t *testing.T) {
	dir := provision(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "file-crash", dir)
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 91 {
		t.Fatal("publish crash not reached", err)
	}
	var e Epoch
	if err := readPrivate(filepath.Join(dir, "epoch-copy.json"), &e); err != nil || e.Number != 1 {
		t.Fatal("published file left a hardlink wedge", err)
	}
}

func TestEachRoleWorksWithoutReadingPeerPrivateKey(t *testing.T) {
	dir := provision(t)
	if err := os.Chmod(filepath.Join(dir, "daemon-key.json"), 0000); err != nil {
		t.Fatal(err)
	}
	s, err := Listen(t.Context(), dir)
	if err != nil {
		t.Fatal("keeper read daemon private key", err)
	}
	_ = s.Close()
	if err := os.Chmod(filepath.Join(dir, "daemon-key.json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "keeper-key.json"), 0000); err != nil {
		t.Fatal(err)
	}
	a, err := AcquireDaemon(dir)
	if err != nil {
		t.Fatal("daemon read keeper private key", err)
	}
	_ = a.Close()
}

func TestSixteenSlotLimitAndClientRetriesBusyPeer(t *testing.T) {
	dir := provision(t)
	path := filepath.Join(dir, "keeper.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer func() { _ = l.Close() }()
	s := &Server{stop: cancel}
	entered := make(chan struct{}, 17)
	release := make(chan struct{})
	ended := make(chan struct{})
	go func() {
		s.acceptConnections(ctx, l.AcceptUnix, waitRetry, func(*net.UnixConn) error { entered <- struct{}{}; <-release; return nil })
		s.workers.Wait()
		close(ended)
	}()
	defer func() { close(release); cancel(); _ = l.Close(); <-ended }()
	var peers []*net.UnixConn
	defer func() {
		for _, peer := range peers {
			_ = peer.Close()
		}
	}()
	for range 16 {
		c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, c)
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("slot never admitted")
		}
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err = c.Read(b[:]); !errors.Is(err, io.EOF) {
		t.Fatal("seventeenth peer was not refused", err)
	}
}

func TestClientRetriesBeforeProvenHandshake(t *testing.T) {
	dir, s, epoch := keeperFixture(t)
	_ = s.Close()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "keeper.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if err = os.Chmod(filepath.Join(dir, "keeper.sock"), 0600); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() {
		// Simulate a full pre-auth slot queue closing the first attempt.
		c, err := l.AcceptUnix()
		if err != nil {
			ended <- err
			return
		}
		_ = c.Close()
		c, err = l.AcceptUnix()
		if err != nil {
			ended <- err
			return
		}
		defer func() { _ = c.Close() }()
		ended <- s.hello(c)
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := RequireEmpty(ctx, dir, epoch); err != nil {
		_ = l.Close()
		t.Fatal("busy peer fenced real daemon instead of retrying", err)
	}
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
}

func TestFenceRecoversFromSixteenRealSilentPeers(t *testing.T) {
	dir, s, epoch := keeperFixture(t)
	_ = s.Close()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "keeper.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(dir, "keeper.sock"), 0600); err != nil {
		_ = l.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	s.stop = cancel
	admitted := make(chan struct{})
	ended := make(chan struct{})
	go func() {
		calls := 0
		s.acceptConnections(ctx, func() (*net.UnixConn, error) {
			calls++
			if calls == 17 {
				close(admitted)
			}
			return l.AcceptUnix()
		}, waitRetry, s.hello)
		s.workers.Wait()
		close(ended)
	}()
	var peers []*net.UnixConn
	defer func() {
		for _, c := range peers {
			_ = c.Close()
		}
		cancel()
		_ = l.Close()
		<-ended
	}()
	for range 16 {
		c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "keeper.sock"), Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, c)
	}
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("silent peers not admitted")
	}
	if err := RequireEmpty(t.Context(), dir, epoch); err != nil {
		t.Fatal("sixteen silent peers wedged fence", err)
	}
}

func TestWireLimitRejectsValidOversizedJSON(t *testing.T) {
	m := message{Proof: transcript{ClientNonce: strings.Repeat("a", wireLimit)}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the real scanner, not just the writer's size guard.
	w := newWireIO(bytes.NewReader(append(data, '\n')), io.Discard)
	if _, err = w.read(); err == nil {
		t.Fatal("oversized valid frame accepted")
	}
	var out bytes.Buffer
	w.out = &out
	if w.write(m) == nil || out.Len() != 0 {
		t.Fatal("oversized outbound frame written")
	}
}

func TestServerRejectsInvalidEpochBeforeIssuingChallenge(t *testing.T) {
	dir, _, epoch := keeperFixture(t)
	epoch.Signature = []byte("invalid")
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "keeper.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	w := newWire(c)
	if err = w.write(clientHello(t, epoch)); err != nil {
		t.Fatal(err)
	}
	if _, err = w.read(); err == nil {
		t.Fatal("invalid epoch elicited signed challenge")
	}
}
