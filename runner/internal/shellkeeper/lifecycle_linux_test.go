//go:build linux

package shellkeeper

import (
	"errors"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type closedAuthWriter struct{}

func (closedAuthWriter) Write([]byte) (int, error) { return 0, net.ErrClosed }

func TestPostChallengeAuthWriteClosureIsBusy(t *testing.T) {
	_, err := finishClient(newWireIO(strings.NewReader(""), closedAuthWriter{}), transcript{}, Identity{}, nil)
	if !errors.Is(err, errBusy) {
		t.Fatal("auth write closure must retry", err)
	}
}

func TestUpdateAbsentKeeperAndUnsafeLock(t *testing.T) {
	dir := provision(t)
	if err := CheckUpdate(t.Context(), dir); err != nil {
		t.Fatal("retry without live keeper wedged", err)
	}
	if err := os.Chmod(filepath.Join(dir, "keeper.lock"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CheckUpdate(t.Context(), dir); err == nil {
		t.Fatal("unsafe lock became absence proof")
	}
}

func TestSignedDrainReplacementAndBuildFence(t *testing.T) {
	for _, name := range []string{"same_live", "different_live", "different_empty", "custody_after_inspect"} {
		t.Run(name, func(t *testing.T) {
			dir, s, epoch := keeperFixture(t)
			s.mu.Lock()
			if name != "same_live" {
				s.build = strings.Repeat("f", 64)
			}
			calls := 0
			s.snapshot = func() []Candidate {
				calls++
				if name == "different_empty" || (name == "custody_after_inspect" && calls == 1) {
					return nil
				}
				return []Candidate{{Allocation: "a", Custody: "c", Shell: "s"}}
			}
			s.mu.Unlock()
			if name == "different_empty" {
				if err := RequireEmpty(t.Context(), dir, epoch); err != nil {
					t.Fatal("compatible different build refused", err)
				}
			}
			err := CheckUpdate(t.Context(), dir)
			wantOK := name != "custody_after_inspect"
			if (err == nil) != wantOK {
				t.Fatalf("update %s: %v", name, err)
			}
			if name == "different_empty" {
				select {
				case <-s.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("drained keeper retained singleton")
				}
				if !s.Drained() {
					t.Fatal("replacement did not use authenticated drain")
				}
				next, err := Listen(t.Context(), dir)
				if err != nil {
					t.Fatal("replacement cannot acquire custody", err)
				}
				defer func() { _ = next.Close() }()
				if err = RequireEmpty(t.Context(), dir, epoch); err != nil {
					t.Fatal("new build still fenced", err)
				}
			} else {
				if s.Drained() {
					t.Fatal("keeper with custody drained")
				}
				if _, err := Inspect(t.Context(), dir, epoch); err != nil {
					t.Fatal("refused update damaged live keeper", err)
				}
			}
		})
	}
}

func TestEpochExhaustionLeavesDurableStateAndLockReusable(t *testing.T) {
	dir := provision(t)
	i, err := loadDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := Epoch{Number: math.MaxUint64, ID: strings.Repeat("e", 64)}
	e.Signature = sign(i.Daemon, "epoch", epochBody(e))
	if err = writePrivate(dir, "epoch.json", e, true); err != nil {
		t.Fatal(err)
	}
	if a, err := AcquireDaemon(dir); err == nil {
		_ = a.Close()
		t.Fatal("exhausted epoch wrapped")
	}
	durable, err := loadEpoch(dir)
	if err != nil || durable.Number != e.Number {
		t.Fatal("exhaustion changed durable authority", err)
	}
	lock, err := acquireLock(dir, "daemon.lock")
	if err != nil {
		t.Fatal("exhaustion leaked lock", err)
	}
	_ = lock.Close()
}

func TestServerMemoryRollbackAndDurableNextEpoch(t *testing.T) {
	dir, s, epoch := keeperFixture(t)
	s.mu.Lock()
	s.epoch.Number = epoch.Number + 1
	s.mu.Unlock()
	if _, err := Inspect(t.Context(), dir, epoch); err == nil {
		t.Fatal("in-memory authority rolled back")
	}
	s.mu.Lock()
	s.epoch = epoch
	s.mu.Unlock()
	i, err := loadDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	next := Epoch{Number: epoch.Number + 1, ID: strings.Repeat("a", 64)}
	next.Signature = sign(i.Daemon, "epoch", epochBody(next))
	if err = writePrivate(dir, "epoch.json", next, true); err != nil {
		t.Fatal(err)
	}
	if _, err = Inspect(t.Context(), dir, next); err != nil {
		t.Fatal("durable next authority refused", err)
	}
	s.mu.Lock()
	got := s.epoch
	s.mu.Unlock()
	if got.Number != next.Number || got.ID != next.ID {
		t.Fatal("accepted authority not retained")
	}
}

func TestStdioProtectsBeforeHelperStart(t *testing.T) {
	// PR_SET_DUMPABLE is process-wide: isolate the oracle from the test runner.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "stdio-protect-oracle")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stdio protection: %v %s", err, out)
	}
}

func stdioProtectOracle() int {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0); err != nil {
		return 92
	}
	observed := false
	stdio(func(_ *exec.Cmd, _ <-chan struct{}) error {
		value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
		observed = err == nil && value == 0
		return errors.New("fixture stops before helper exec")
	})
	if !observed {
		return 93
	}
	return 0
}
