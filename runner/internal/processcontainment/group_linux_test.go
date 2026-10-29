//go:build linux

package processcontainment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "threaded-writer" {
		var ready sync.WaitGroup
		ready.Add(4)
		for i := range 4 {
			go func() {
				runtime.LockOSThread()
				ready.Done()
				for {
					_ = os.WriteFile(os.Args[2]+strconv.Itoa(i), []byte(time.Now().String()), 0600)
					time.Sleep(time.Millisecond)
				}
			}()
		}
		ready.Wait()
		for {
			_ = os.WriteFile(os.Args[2], []byte(time.Now().String()), 0600)
			time.Sleep(time.Millisecond)
		}
	}

	if len(os.Args) > 1 && os.Args[1] == "cluster-writer" {
		if os.Getenv("P6_GROUP_DROP_START_ACK") == "1" {
			time.Sleep(10 * time.Second)
			os.Exit(125)
		}
		os.Exit(ServeCommand(os.Args[2:]))
	}
	os.Exit(m.Run())
}
func until(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestGroupFreezesDetachedWritersAndProvesStop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heartbeat")
	pidPath := filepath.Join(dir, "pid")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	// setsid makes the descendant independent of its parent's process group.
	cmd := exec.Command("sh", "-c", `setsid sh -c 'echo $$ > "$2"; while :; do echo x >> "$1"; sleep 0.005; done' sh "$1" "$2" & wait`, "sh", path, pidPath)
	if err = group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	defer func() { _ = group.Stop(ctx) }()
	until(t, func() bool { s, e := os.Stat(path); return e == nil && s.Size() > 2 })
	pidRaw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Freeze(ctx); err != nil {
		t.Fatal("freeze:", err)
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(status), "tracing stop") {
		t.Fatalf("detached writer was not kernel-stopped: %s", status)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	after, err := os.Stat(path)
	if err != nil || after.Size() != before.Size() {
		t.Fatal("writer advanced during freeze")
	}
	if group.Start(exec.Command("true")) == nil {
		t.Fatal("admitted writer during freeze")
	}
	if err = group.Thaw(ctx); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { s, e := os.Stat(path); return e == nil && s.Size() > after.Size() })
	if err = group.Stop(ctx); err != nil {
		t.Fatal("stop:", err)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("detached writer survived empty receipt")
	}
	if group.Start(exec.Command("true")) == nil {
		t.Fatal("admitted writer after stop")
	}
}
func TestGroupLostHelperNeverInventsEmptyReceipt(t *testing.T) {
	binary, _ := os.Executable()
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command("sleep", "60")
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := group.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := group.Stop(ctx); err == nil {
		t.Fatal("lost helper fabricated cleanup receipt")
	}
}

func TestGroupCommandCancellationHasExactReceipt(t *testing.T) {
	binary, _ := os.Executable()
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "60")
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = cmd.Wait()
	cleanup, done := context.WithTimeout(t.Context(), 5*time.Second)
	defer done()
	if err := group.Stop(cleanup); err != nil {
		t.Fatal("normal command cancellation lost its cleanup receipt", err)
	}
}

func TestHelperEnvironmentExcludesLoaderInjection(t *testing.T) {
	input := []string{"LD_PRELOAD=/tmp/fixture.so", "LD_LIBRARY_PATH=/tmp/fixture", "GCONV_PATH=/tmp/fixture", "GLIBC_TUNABLES=fixture", "HOME=/workspace", "LANG=C.UTF-8"}
	got := HelperEnvironment(input)
	if strings.Join(got, "\n") != "HOME=/workspace\nLANG=C.UTF-8" {
		t.Fatal("trusted helper inherited loader injection")
	}
}

// A durable owner record must exist before the helper can execute a writer.
func TestGroupJournalFailurePreventsExec(t *testing.T) {
	binary, _ := os.Executable()
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	marker := filepath.Join(t.TempDir(), "started")
	group.BeforeExec = func(int) error { return fmt.Errorf("journal unavailable") }
	cmd := exec.Command("sh", "-c", `echo unsafe > "$1"`, "sh", marker)
	if err := group.Start(cmd); err == nil {
		_ = cmd.Wait()
		t.Fatal("JOURNAL_BEFORE_EXEC: admitted an unrecorded writer")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("JOURNAL_BEFORE_EXEC: writer ran before durable identity")
	}
}

func TestGroupLostStartAckNeverInventsEmpty(t *testing.T) {
	t.Setenv("P6_GROUP_DROP_START_ACK", "1")
	binary, _ := os.Executable()
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command("true")
	if err := group.Start(cmd); err == nil {
		t.Fatal("lost startup reply accepted")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := group.Stop(ctx); err == nil {
		t.Fatal("START_ACK_PROOF: lost startup reply invented empty containment")
	}
}

// An app-server remains alive between turns and has multiple kernel threads.
func TestGroupFreezesLongLivedThreadedEngine(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "heartbeat")
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command(binary, "threaded-writer", path)
	if err = group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = group.Stop(ctx)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	until(t, func() bool {
		for _, suffix := range []string{"", "0", "1", "2", "3"} {
			if data, err := os.ReadFile(path + suffix); err != nil || len(data) == 0 {
				return false
			}
		}
		return true
	})
	for range 3 {
		ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
		err = group.Freeze(ctx)
		cancel()
		if err != nil {
			t.Fatal("freeze live threaded engine:", err)
		}
		snapshots := map[string]string{}
		for _, suffix := range []string{"", "0", "1", "2", "3"} {
			data, err := os.ReadFile(path + suffix)
			if err != nil {
				t.Fatal(err)
			}
			snapshots[suffix] = string(data)
		}
		time.Sleep(30 * time.Millisecond)
		for suffix, before := range snapshots {
			after, err := os.ReadFile(path + suffix)
			if err != nil || before != string(after) {
				t.Fatal("engine thread wrote while frozen:", suffix, err)
			}
		}
		ctx, cancel = context.WithTimeout(t.Context(), 7*time.Second)
		err = group.Thaw(ctx)
		cancel()
		if err != nil {
			t.Fatal("thaw live threaded engine:", err)
		}
		until(t, func() bool {
			for suffix, before := range snapshots {
				after, _ := os.ReadFile(path + suffix)
				if len(after) == 0 || before == string(after) {
					return false
				}
			}
			return true
		})
	}
}

func TestGroupReportsFailedTreeCheck(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command("sleep", "60")
	if err = group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := group.Stop(ctx); err != nil {
			t.Error("stop:", err)
		}
		_ = cmd.Wait()
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	err = group.control(ctx, "unsupported")
	if !errors.Is(err, ErrUnproven) || !strings.Contains(err.Error(), "writer helper 1") || !strings.Contains(err.Error(), "unsupported tree control operation") {
		t.Fatalf("failed tree check did not reach group caller: %v", err)
	}
}
