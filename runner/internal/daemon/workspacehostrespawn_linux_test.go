//go:build linux

package daemon

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
)

// Exercise the actual streaming driver, helper protocol, idle closure, and next-message respawn.
func TestHostSteeringIdleRespawnFreeze(t *testing.T) {
	binary := os.Getenv("BLERG_HOST_HELPER_TEST_BINARY")
	if binary == "" {
		t.Skip("built daemon helper required")
	}
	logs := captureLinkLog(t)
	shrink(t, &ccIdleShutdown, 150*time.Millisecond)
	d, em, _ := newSteeringDriver(t)
	group := processcontainment.NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	d.useCommandBoundary(group.Start)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := group.Stop(ctx); err != nil {
			t.Error("writer cleanup:", err)
		}
	}()
	for turn := 1; turn <= 2; turn++ {
		d.Enqueue("hello", "chat")
		waitIdle(t, em, turn)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		if err := group.Freeze(ctx); err != nil {
			t.Error("freeze turn", turn, err)
		}
		if err := group.Thaw(ctx); err != nil {
			t.Error("thaw turn", turn, err)
		}
		cancel()
		waitUntil(t, "idle engine exit", func() bool { d.sm.Lock(); defer d.sm.Unlock(); return d.steer.proc == nil })
	}
	waitLinkLog(t, logs, "claude-code: idle shutdown:")
	waitLinkLog(t, logs, "claude-code: process exited:")
	waitLinkLog(t, logs, "tree ended with an empty receipt")
	if em.count("error") != 0 {
		t.Fatalf("respawn errors: %v", em.outline())
	}
}

// A lost helper never ends the session: its tree is proven gone, the turn-end freeze and thaw
// succeed, and the next message starts the engine under a fresh helper.
func TestHostSteeringSurvivesLostHelper(t *testing.T) {
	binary := os.Getenv("BLERG_HOST_HELPER_TEST_BINARY")
	if binary == "" {
		t.Skip("built daemon helper required")
	}
	for _, midTurn := range []bool{false, true} {
		t.Run(fmt.Sprintf("mid-turn=%t", midTurn), func(t *testing.T) {
			logs := captureLinkLog(t)
			d, em, _ := newSteeringDriver(t)
			group := processcontainment.NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
			d.useCommandBoundary(group.Start)
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := group.Stop(ctx); err != nil {
					t.Error("writer cleanup:", err)
				}
			}()
			turnEnd := func(what string) {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				if err := group.Freeze(ctx); err != nil {
					t.Fatal("freeze", what, err)
				}
				if err := group.Thaw(ctx); err != nil {
					t.Fatal("thaw", what, err)
				}
			}
			helper := func() int {
				d.sm.Lock()
				defer d.sm.Unlock()
				if d.steer.proc == nil || d.steer.proc.cmd == nil || d.steer.proc.cmd.Process == nil {
					return 0
				}
				return d.steer.proc.cmd.Process.Pid
			}
			d.Enqueue("hello", "chat")
			waitIdle(t, em, 1)
			turnEnd("first turn")
			if midTurn {
				d.Enqueue("HANG until the helper is lost", "chat")
				waitUntil(t, "the hanging turn", func() bool { return em.count("user:HANG") == 1 })
			}
			old := helper()
			if old == 0 {
				t.Fatal("no live helper")
			}
			if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "the engine exit", func() bool { return helper() != old })
			waitLinkLog(t, logs, "its processes are proven gone")
			turnEnd("after the helper was lost")
			turns := em.count("turn_done")
			d.Enqueue("again", "chat")
			waitUntil(t, "a turn under a fresh helper", func() bool { return em.count("turn_done") > turns && em.count("text:ok:again") == 1 })
			if fresh := helper(); fresh == 0 || fresh == old {
				t.Fatalf("engine not restarted under a fresh helper: %d (lost %d)", fresh, old)
			}
			turnEnd("after the respawn")
		})
	}
}
