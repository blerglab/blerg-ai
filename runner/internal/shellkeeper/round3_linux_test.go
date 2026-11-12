//go:build linux

package shellkeeper

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func localPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]*net.UnixConn, 0, 2)
	for _, fd := range fds {
		f := os.NewFile(uintptr(fd), "keeper-test")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c.(*net.UnixConn))
	}
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return conns[0], conns[1]
}

func TestDrainGuardsAndLostAcknowledgment(t *testing.T) {
	for _, name := range []string{"draining", "lost_ack", "durable_epoch", "unknown_operation"} {
		t.Run(name, func(t *testing.T) {
			dir := provision(t)
			i, err := LoadIdentity(dir)
			if err != nil {
				t.Fatal(err)
			}
			epoch, err := loadEpoch(dir)
			if err != nil {
				t.Fatal(err)
			}
			stopped := make(chan struct{}, 1)
			snapshots := 0
			s := &Server{dir: dir, identity: i, epoch: epoch, generation: strings.Repeat("a", 64), build: strings.Repeat("b", 64), stop: func() { stopped <- struct{}{} }, snapshot: func() []Candidate { snapshots++; return nil }}
			s.draining = name == "draining"
			server, client := localPair(t)
			ended := make(chan error, 1)
			go func() { ended <- s.hello(server); _ = server.Close() }()
			_ = client.SetDeadline(time.Now().Add(3 * time.Second))
			w := newWire(client)
			hello := clientHello(t, epoch)
			hello.Proof.Operation = "drain"
			hello.Proof.Version = maintenanceVersion
			if name == "unknown_operation" {
				hello.Proof.Operation = "unrecognized"
				hello.Proof.Version = Version
			}
			if err = w.write(hello); err != nil {
				t.Fatal(err)
			}
			challenge, err := w.read()
			if name == "unknown_operation" {
				if err == nil {
					t.Fatal("unknown operation received challenge")
				}
				if err = <-ended; err == nil {
					t.Fatal("unknown operation accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "durable_epoch" {
				next := epoch
				next.Number++
				next.ID = strings.Repeat("c", 64)
				next.Signature = sign(i.Daemon, "epoch", epochBody(next))
				if err = writePrivate(dir, "epoch.json", next, true); err != nil {
					t.Fatal(err)
				}
			}
			if name == "lost_ack" {
				if err = client.CloseRead(); err != nil {
					t.Fatal(err)
				}
			}
			if err = w.write(message{Proof: challenge.Proof, Signature: sign(i.Daemon, "daemon", challenge.Proof)}); err != nil {
				t.Fatal(err)
			}
			_ = client.CloseWrite()
			err = <-ended
			if err == nil {
				t.Fatal("failure path did not fail")
			}
			select {
			case <-stopped:
				if name != "lost_ack" {
					t.Fatal("refused drain stopped keeper")
				}
			default:
				if name == "lost_ack" {
					t.Fatal("lost ACK retained draining keeper")
				}
			}
			if name == "draining" && snapshots != 0 {
				t.Fatal("draining keeper continued request")
			}
			if name == "durable_epoch" && s.draining {
				t.Fatal("stale durable epoch drained keeper")
			}
		})
	}
}

func TestLostAckMustProveAbsenceAndReleaseWaits(t *testing.T) {
	marker := errors.New("lost authenticated acknowledgment")
	probes := 0
	err := finishDrain(t.Context(), marker, func() (bool, error) { probes++; return false, nil }, func(context.Context, time.Duration) bool { t.Fatal("unproven ACK entered release wait"); return false })
	if !errors.Is(err, marker) || probes != 1 {
		t.Fatal("lost ACK succeeded without absence proof", err, probes)
	}
	dir := provision(t)
	held, err := acquireLock(dir, "keeper.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	waits := 0
	err = finishDrain(t.Context(), nil, func() (bool, error) { return keeperAbsent(dir) }, func(context.Context, time.Duration) bool { waits++; _ = held.Close(); return true })
	if err != nil || waits != 1 {
		t.Fatal("release wait bypassed or abandoned real flock", err, waits)
	}
}

func TestAbsenceProofRejectsReplacedLockInode(t *testing.T) {
	dir := provision(t)
	f, err := tryKeeperLock(dir, func(_ *os.File) {
		path := filepath.Join(dir, "keeper.lock")
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	})
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("unlocked pathname became false absence proof")
	}
}

func TestInstallDoesNotAcquireSingletonAcrossBinarySwap(t *testing.T) {
	dir := provision(t)
	target := filepath.Join(t.TempDir(), "daemon")
	if err := os.WriteFile(target, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	checked := false
	err := installAt(t.Context(), dir, target, func() {
		checked = true
		if absent, err := keeperAbsent(dir); err != nil || !absent {
			t.Fatal("installer acquired custody lock", err)
		}
		if old, err := Listen(t.Context(), dir); err == nil {
			_ = old.Close()
		} else {
			t.Fatal("install prevented keeper start", err)
		}
	})
	if err != nil || !checked {
		t.Fatal("locked install", err)
	}
	digest, err := fileDigest(target)
	build, buildErr := BuildID()
	if err != nil || buildErr != nil || digest != build {
		t.Fatal("installed image is not pinned executable", err, buildErr)
	}
	if absent, err := keeperAbsent(dir); err != nil || !absent {
		t.Fatal("install leaked singleton", err)
	}
}

func TestFrozenMaintenanceDrainAndGeneration(t *testing.T) {
	dir, s, epoch := keeperFixture(t)
	view, err := requestVersion(t.Context(), dir, epoch, "update", Version+1)
	if err != nil || view.Generation != s.generation {
		t.Fatal("maintenance snapshot lost generation", err, view.Generation)
	}
	if _, err = requestVersion(t.Context(), dir, epoch, "", Version+1); err == nil {
		t.Fatal("normal custody accepted future protocol version")
	}
	if err = Drain(t.Context(), dir); err != nil {
		t.Fatal("working same-build version-independent drain", err)
	}
	if !s.Drained() {
		t.Fatal("operator drain left matching keeper alive")
	}
}

func TestIdenticalInstallRetainsLiveCustodyWithoutSwap(t *testing.T) {
	dir, s, _ := keeperFixture(t)
	target := filepath.Join(t.TempDir(), "daemon")
	image, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, image, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.snapshot = func() []Candidate { return []Candidate{{Allocation: "a", Custody: "c", Shell: "s"}} }
	s.mu.Unlock()
	if err = installAt(t.Context(), dir, target, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil || os.SameFile(before, after) {
		t.Fatal("atomic pathname replacement missing", err)
	}
	if s.Drained() {
		t.Fatal("identical reinstall drained live custody")
	}
}
