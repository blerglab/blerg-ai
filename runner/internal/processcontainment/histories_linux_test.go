//go:build linux

package processcontainment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGroupFreezesAfterThreadHistory(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Fatal("C compiler required for pthread/clone containment regressions:", err)
	}
	fixture := filepath.Join(t.TempDir(), "histories")
	if out, err := exec.Command(cc, "-pthread", "-o", fixture, "testdata/histories.c").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v: %s", err, out)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, history := range []string{"leader-exit", "leader-exec", "exec-thread", "thread-exit", "vfork-exec", "vfork-exit", "vfork-delay", "orphan", "storm"} {
		t.Run(history, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ready")
			group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
			cmd := exec.Command(fixture, history, marker)
			cmd.Stderr = os.Stderr
			if err := group.Start(cmd); err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := group.Stop(ctx); err != nil {
					t.Error("cleanup:", err)
					_ = cmd.Process.Kill()
				}
				_ = cmd.Wait()
			}()
			untilWithin(t, time.Minute, func() bool { _, err := os.Stat(marker); return err == nil })

			freeze := func() {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				if err := group.Freeze(ctx); err != nil {
					t.Fatal("freeze after history:", err)
				}
			}
			thaw := func() {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				if err := group.Thaw(ctx); err != nil {
					t.Fatal("thaw after history:", err)
				}
			}
			// Exercise vfork both in flight and after the child has exited. For other histories,
			// the heartbeat proves a surviving thread can still write between freezes.
			if strings.HasPrefix(history, "vfork-") {
				for range 20 {
					freeze()
					thaw()
					time.Sleep(time.Millisecond)
				}
			}
			until(t, func() bool { data, _ := os.ReadFile(marker + ".beat"); return len(data) != 0 })
			for range 3 {
				freeze()
				before, err := os.ReadFile(marker + ".beat")
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(20 * time.Millisecond)
				after, err := os.ReadFile(marker + ".beat")
				if err != nil || string(before) != string(after) {
					t.Fatal("writer advanced while frozen", err)
				}
				thaw()
				until(t, func() bool {
					data, _ := os.ReadFile(marker + ".beat")
					return len(data) != 0 && string(data) != string(before)
				})
			}
			if history == "leader-exec" {
				freeze()
				if err := os.WriteFile(marker+".exec", nil, 0600); err != nil {
					t.Fatal(err)
				}
				time.Sleep(20 * time.Millisecond)
				if data, _ := os.ReadFile(marker); string(data) != "ready" {
					t.Fatal("exec ran while sibling frozen")
				}
				thaw()
				until(t, func() bool { data, _ := os.ReadFile(marker); return string(data) == "exec-ready" })
				freeze()
				thaw()
				// Repeated freezes must stop the replacement program too, not reuse the leader's EXIT proof.
				for range 3 {
					freeze()
					before, _ := os.ReadFile(marker + ".beat")
					time.Sleep(20 * time.Millisecond)
					after, err := os.ReadFile(marker + ".beat")
					if err != nil || string(before) != string(after) {
						t.Fatal("exec replacement ran while frozen", err)
					}
					thaw()
				}
			}
		})
	}
}

// Many short-lived processes and threads leave nothing behind in the tracer's accounting: a frozen
// tree tracks exactly the tasks the kernel says it traces. A leaked entry would also block every
// freeze and count toward the task limit until the tree was killed.
func TestTreeAccountingMatchesKernelAfterSpawnStorm(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Fatal("C compiler required:", err)
	}
	fixture := filepath.Join(t.TempDir(), "histories")
	if out, err := exec.Command(cc, "-pthread", "-o", fixture, "testdata/histories.c").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v: %s", err, out)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for round := range 3 {
		marker := filepath.Join(t.TempDir(), "ready")
		out, err := exec.Command(binary, "tree-census", fixture, "storm", marker).CombinedOutput()
		if err != nil {
			t.Fatalf("round %d: census child: %v: %s", round, err, out)
		}
		text := string(out)
		if !strings.Contains(text, "freeze: <nil>") || !strings.Contains(text, "stop: <nil>") {
			t.Fatalf("round %d: %s", round, text)
		}
		var tracked, live int
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "census ") {
				_, _ = fmt.Sscanf(line, "census tracked=%d live=%d", &tracked, &live)
			}
		}
		if tracked == 0 || tracked != live {
			t.Fatalf("round %d: tracer tracks %d tasks, kernel traces %d: %s", round, tracked, live, text)
		}
		if testing.Verbose() {
			t.Log(text)
		}
	}
}
