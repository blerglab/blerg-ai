//go:build linux

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
)

func TestSessionEndToEndProcessShapes(t *testing.T) {
	if os.Getenv("BLERG_HOST_HELPER_TEST_BINARY") == "" {
		t.Skip("built daemon helper required")
	}
	hostChildrenConcurrentCommits(t, stressHostWriter)
}

func stressHostWriter(t *testing.T, w *LocalWorkspace, cwd string, report func()) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sessionstub")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/sessionstub").CombinedOutput(); err != nil {
		t.Fatalf("stub: %v: %s", err, out)
	}
	histories := filepath.Join(t.TempDir(), "histories")
	if out, err := exec.Command("cc", "-pthread", "-o", histories, "../processcontainment/testdata/histories.c").CombinedOutput(); err != nil {
		t.Fatalf("histories: %v: %s", err, out)
	}
	for _, mode := range []string{"leader-exit", "many-threads", "vfork-exec", "vfork-exit", "posix-spawn", "storm", "go-exec", "idle-exit", "group-kill"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ready")
			cmd := exec.Command(bin, histories, mode, marker)
			cmd.Dir, cmd.Stderr = cwd, os.Stderr
			if err := w.host.writers.Start(cmd); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() {
				_ = os.WriteFile(marker+".stop", nil, 0600)
				select {
				case err := <-done:
					if err != nil {
						t.Error("stub exit:", err)
					}
				case <-time.After(5 * time.Second):
					_ = cmd.Process.Kill() // the writer helper: EXITKILL ends the stub with it
					<-done
					t.Error("stub failed to exit")
				}
			}()
			// A failed turn must not leave the group frozen: a frozen group admits no command, so the
			// stub could not exit and every later scenario would be refused.
			frozen := false
			defer func() {
				if frozen {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if err := w.host.writers.Thaw(ctx); err != nil {
						t.Error("thaw after a failed turn:", err)
					}
				}
			}()
			waitUntilFor(t, time.Minute, "stub readiness", func() bool { _, err := os.Stat(marker); return err == nil })
			for turn := range 5 {
				name := fmt.Sprintf("%s-%d.txt", mode, turn)
				workspaceFixture(t, cwd, name, name)
				childRepoGit(t, cwd, "add", name)
				childRepoGit(t, cwd, "commit", "-qm", name)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				if _, err := w.Sync(ctx); err != nil {
					t.Fatal("turn sync:", err)
				}
				frozen = true
				if err := w.host.writers.Freeze(ctx); err != nil {
					t.Fatal(err)
				}
				counts, err := w.host.writers.(*processcontainment.Group).Census(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range counts {
					// Exact at one instant of the tracer: every live task is tracked, and every tracked
					// task the kernel no longer has had committed to exit.
					if c.Untracked != 0 || c.Lost != 0 || c.Tracked-c.Exited != c.Live {
						t.Errorf("census tracked=%d live=%d untracked=%d exited=%d lost=%d: %s", c.Tracked, c.Live, c.Untracked, c.Exited, c.Lost, c.Mismatch)
					}
				}
				if err := w.host.writers.Thaw(ctx); err != nil {
					t.Fatal(err)
				}
				frozen = false
				report()
			}

		})
	}
}
