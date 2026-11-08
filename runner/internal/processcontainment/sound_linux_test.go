//go:build linux

package processcontainment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func histories(t *testing.T) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "histories")
	if out, err := exec.Command("cc", "-pthread", "-o", fixture, "testdata/histories.c").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v: %s", err, out)
	}
	return fixture
}

// heldChildren runs a tree whose tracer continues the root but no other process. A child's first
// report is then a barrier or it is not: any byte in .ran is user code that no PTRACE_CONT released.
func heldChildren(fixture, mode, marker string) {
	cmd := exec.Command(fixture, mode, marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	tree, ready := NewTree(), make(chan error, 1)
	root := atomic.Int64{}
	original := continueTask
	continueTask = func(pid, sig int) error {
		if tgid := threadGroup(pid); tgid != 0 && tgid == int(root.Load()) {
			return original(pid, sig)
		}
		return nil
	}
	go func() {
		_, err := tree.Run(cmd, func() error {
			err := cmd.Start()
			if err == nil {
				root.Store(int64(cmd.Process.Pid))
			}
			return err
		}, WriterTaskLimit, ready)
		fmt.Println("run:", err)
	}()
	if err := <-ready; err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
	ran, _ := os.ReadFile(marker + ".ran")
	fmt.Printf("ran=%d\n", len(ran))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	fmt.Println("stop:", tree.Stop(ctx))
	cancel()
	os.Exit(0)
}

// A new child runs nothing until the tracer continues it, with SIGCONT raining on its process group
// from before it exists, and whether or not it has SIGCONT blocked.
func TestNewChildRunsNothingBeforeItsContinue(t *testing.T) {
	binary, _ := os.Executable()
	fixture := histories(t)
	for _, mode := range []string{"sigcont-children", "sigcont-children-blocked"} {
		t.Run(mode, func(t *testing.T) {
			out, err := exec.Command(binary, "tree-held-children", fixture, mode, filepath.Join(t.TempDir(), "ready")).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "ran=0\n") {
				t.Fatalf("a child ran without a continue: %v: %s", err, out)
			}
		})
	}
}

// A freeze survives SIGCONT sent by anything at any time: it acknowledges, and it holds.
func TestFreezeHoldsThroughSIGCONT(t *testing.T) {
	fixture := histories(t)
	for _, mode := range []string{"pid-beat", "kernel-beat"} {
		t.Run(mode, func(t *testing.T) { freezeThroughSIGCONT(t, fixture, mode) })
	}
}

func freezeThroughSIGCONT(t *testing.T, fixture, mode string) {
	binary, _ := os.Executable()
	marker := filepath.Join(t.TempDir(), "ready")
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command(fixture, mode, marker)
	cmd.Stderr = os.Stderr
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = group.Stop(ctx)
		_ = cmd.Wait()
	}()
	until(t, func() bool { _, err := os.Stat(marker); return err == nil })
	raw, err := os.ReadFile(marker + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(raw))
	// While the freeze lands and holds. Left on for the thaw, it would starve the task: every SIGCONT
	// to a seized task is a report, so the beat could not advance.
	stop := make(chan struct{})
	defer close(stop)
	var storm atomic.Bool
	for range 2 {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					if storm.Load() {
						_ = unix.Kill(pid, unix.SIGCONT)
					} else {
						time.Sleep(100 * time.Microsecond)
					}
				}
			}
		}()
	}
	for i := range 20 {
		storm.Store(true)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		err := group.Freeze(ctx)
		cancel()
		if err != nil {
			t.Fatalf("freeze %d under SIGCONT: %v", i, err)
		}
		before, _ := os.ReadFile(marker + ".beat")
		time.Sleep(10 * time.Millisecond)
		if after, _ := os.ReadFile(marker + ".beat"); string(after) != string(before) {
			t.Fatalf("freeze %d: ran under SIGCONT", i)
		}
		storm.Store(false)
		ctx, cancel = context.WithTimeout(t.Context(), 3*time.Second)
		err = group.Thaw(ctx)
		cancel()
		if err != nil {
			t.Fatalf("thaw %d: %v", i, err)
		}
		until(t, func() bool { after, _ := os.ReadFile(marker + ".beat"); return string(after) != string(before) })
	}
}

// A process that stops itself is stopped: it runs nothing, a freeze counts it as held, a thaw leaves
// it stopped, and SIGCONT resumes it, at once or at the thaw if a freeze holds it.
func TestTreeOwnStopIsAGroupStop(t *testing.T) {
	binary, _ := os.Executable()
	fixture := histories(t)
	marker := filepath.Join(t.TempDir(), "ready")
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command(fixture, "self-stop", marker)
	cmd.Stderr = os.Stderr
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = group.Stop(ctx)
		_ = cmd.Wait()
	}()
	until(t, func() bool { _, err := os.Stat(marker); return err == nil })
	raw, err := os.ReadFile(marker + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(raw))
	still := func(why string) {
		t.Helper()
		time.Sleep(30 * time.Millisecond)
		if _, err := os.Stat(marker + ".beat"); err == nil {
			t.Fatal("ran while stopped: " + why)
		}
	}
	turn := func(op func(context.Context) error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		if err := op(ctx); err != nil {
			t.Fatal(err)
		}
	}
	still("after its own SIGSTOP")
	turn(group.Freeze)
	still("frozen")
	turn(group.Thaw)
	still("after a thaw: a thaw must not undo the tree's own stop")
	turn(group.Freeze)
	if err := unix.Kill(pid, unix.SIGCONT); err != nil {
		t.Fatal(err)
	}
	still("SIGCONT while frozen")
	turn(group.Thaw)
	until(t, func() bool { _, err := os.Stat(marker + ".beat"); return err == nil })
}

// settledCensus takes a census of a tree no freeze holds. It still runs: its last forks and exits are
// on their way to the tracer, and one census can catch them. Those settle. A lost task stays lost.
func settledCensus(tree *Tree) *Census {
	for settle := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := tree.Control(ctx, "census"); err != nil {
			fmt.Println("census:", err)
		}
		cancel()
		c := tree.census.Load()
		if c != nil && (c.Untracked+c.Lost == 0 || time.Now().After(settle)) {
			return c
		}
	}
}

// tidReuse runs the reuse fixture, then freezes and reports what the tracer and the kernel say.
func tidReuse(fixture, marker string) {
	mode := "tid-reuse"
	if os.Getenv("BLERG_TEST_NO_BARRIER") == "1" {
		// The tracer never sees a thread's seccomp stop, as with a listener's CONTINUE: what any way
		// past the barrier amounts to. Threads stay threads, so teardown is the real one.
		mode = "tid-reuse-short"
		wait, resume := waitTask, continueTask
		waitTask = func(pid int, status *unix.WaitStatus, options int, usage *unix.Rusage) (int, error) {
			reported, err := wait(pid, status, options, usage)
			if err == nil && reported != 0 && status.Stopped() && status.TrapCause() == unix.PTRACE_EVENT_SECCOMP && threadGroup(reported) != reported {
				return 0, resume(reported, 0)
			}
			return reported, err
		}
	}
	cmd := exec.Command(fixture, mode, marker)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	tree, ready := NewTree(), make(chan error, 1)
	go func() { _, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready); fmt.Println("run:", err) }()
	if err := <-ready; err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	time.AfterFunc(120*time.Second, func() { _ = os.WriteFile(marker+".stop", nil, 0600) })
	for deadline := time.Now().Add(180 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if data, _ := os.ReadFile(marker + ".beat"); len(data) != 0 {
			break
		}
		if time.Now().After(deadline) {
			fmt.Println("fixture never finished")
			break
		}
	}
	hits, _ := os.ReadFile(marker + ".hits")
	rounds, _ := os.ReadFile(marker + ".rounds")
	fmt.Printf("rounds=%s reborn=%d barriers=%d bypassed=%d\n", rounds, tree.reborn.Load(), tree.barriers.Load(), tree.bypassed.Load())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := tree.Control(ctx, "freeze")
	cancel()
	fmt.Printf("hits=%s freeze: %v\n", hits, err)
	if c := settledCensus(tree); c != nil {
		fmt.Printf("census tracked=%d live=%d untracked=%d lost=%d %s\n", c.Tracked, c.Live, c.Untracked, c.Lost, c.Mismatch)
	}
	// The helper's own budget. A tree of 200 ends in under 500 ms even at 4 runnable tasks per CPU; only
	// a machine far past that stretches it to seconds, and a tree that really hangs still fails here.
	ctx, cancel = context.WithTimeout(context.Background(), controlTimeout)
	fmt.Println("stop:", tree.Stop(ctx))
	cancel()
	os.Exit(0)
}

// after is what follows the first occurrence of prefix in text.
func after(text, prefix string) string {
	_, rest, _ := strings.Cut(text, prefix)
	return rest
}

// Real TID reuse, forced inside a PID namespace: a task that takes the number of a thread in the
// middle of an exec would be created under a tracked TID. Every round aims forks at that number, so
// reuse happens, and it may only ever happen after the exec's entry is gone.
func TestTIDReuseAcrossPendingExec(t *testing.T) {
	if err := exec.Command("unshare", "-Urpf", "--mount-proc", "/bin/true").Run(); err != nil {
		t.Skip("no unprivileged PID namespace here:", err)
	}
	binary, _ := os.Executable()
	fixture := histories(t)
	// All 1500 rounds take under 90 s on a quiet machine. A loaded one runs out of budget sooner, and
	// every round it finished still counts exactly.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "unshare", "-Urpf", "--mount-proc", binary, "tree-tid-reuse", fixture, filepath.Join(t.TempDir(), "ready")).CombinedOutput()
	text := string(out)
	t.Log(text)
	if err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	if strings.Contains(text, "fixture never finished") {
		t.Fatalf("too slow to finish, which says nothing about reuse: %s", text)
	}
	var rounds, hits int
	if _, err := fmt.Sscanf(after(text, "rounds="), "%d ", &rounds); err != nil || rounds == 0 {
		t.Fatalf("no rounds finished: %v: %s", err, text)
	}
	if !strings.Contains(text, fmt.Sprintf("rounds=%d reborn=0 barriers=%d bypassed=0", rounds, rounds)) {
		t.Fatalf("a TID was reused across a pending exec: %s", text)
	}
	if _, err := fmt.Sscanf(after(text, "hits="), "%d ", &hits); err != nil || hits == 0 {
		t.Fatalf("no number was reused, so this proved nothing: %v: %s", err, text)
	}
	if !strings.Contains(text, "freeze: <nil>") || !strings.Contains(text, "untracked=0 lost=0") || !strings.Contains(text, "stop: <nil>") {
		t.Fatalf("a reused TID broke the accounting: %s", text)
	}
}

// An exec that gets past the barrier, whatever the way, reaches de_thread in a running tree, and
// reused TIDs are real again. No freeze may acknowledge after that, and no task may be lost.
func TestExecPastTheBarrierFailsClosed(t *testing.T) {
	if err := exec.Command("unshare", "-Urpf", "--mount-proc", "/bin/true").Run(); err != nil {
		t.Skip("no unprivileged PID namespace here:", err)
	}
	binary, _ := os.Executable()
	fixture := histories(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unshare", "-Urpf", "--mount-proc", binary, "tree-tid-reuse", fixture, filepath.Join(t.TempDir(), "ready"))
	cmd.Env = append(os.Environ(), "BLERG_TEST_NO_BARRIER=1")
	out, err := cmd.CombinedOutput()
	text := string(out)
	t.Log(text)
	if err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	for _, want := range []string{"barriers=0 bypassed=200", "freeze: " + ErrUnproven.Error(), "untracked=0 lost=0", "stop: <nil>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("want %q: %s", want, text)
		}
	}
}

// pidOfPidfd is the process a pidfd names, from this process's view of /proc.
func pidOfPidfd(fd int) int {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	for line := range strings.Lines(string(data)) {
		if value, ok := strings.CutPrefix(line, "Pid:"); ok {
			pid, _ := strconv.Atoi(strings.TrimSpace(value))
			return pid
		}
	}
	return 0
}

// reportOrder carries one thread's exec past the barrier, as an inherited listener would: the tracer
// never sees its seccomp stop. The tree really reuses the number. Then the three reports that follow
// are held until all have come, and delivered with the new task's own first stop first.
func reportOrder(fixture, marker, order string) {
	cmd := exec.Command(fixture, "tid-reuse-short", marker)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	tree, ready := NewTree(), make(chan error, 1)
	var mu sync.Mutex
	recorded := map[int]int{}
	tree.Ledger = func(fd int) error {
		mu.Lock()
		defer mu.Unlock()
		recorded[pidOfPidfd(fd)]++
		return nil
	}
	type report struct {
		pid    int
		status unix.WaitStatus
	}
	var former int
	var queue []report
	var own, created, execed *report
	delivered := make(chan struct{})
	checked := make(chan string, 1)
	wait, resume := waitTask, continueTask
	waitTask = func(pid int, status *unix.WaitStatus, options int, usage *unix.Rusage) (int, error) {
		if len(queue) != 0 {
			next := queue[0]
			queue = queue[1:]
			*status = next.status
			if len(queue) == 0 {
				close(delivered)
			}
			return next.pid, nil
		}
		reported, err := wait(pid, status, options, usage)
		if err != nil || reported == 0 || !status.Stopped() {
			return reported, err
		}
		cause := status.TrapCause()
		if former == 0 {
			if cause == unix.PTRACE_EVENT_SECCOMP && threadGroup(reported) != reported {
				former = reported
				return 0, resume(reported, 0)
			}
			return reported, err
		}
		if own != nil && created != nil && execed != nil {
			return reported, err
		}
		held := &report{reported, *status}
		switch message, _ := readEventMessage(reported); {
		case reported == former && cause == unix.PTRACE_EVENT_STOP:
			own = held
		case cause == unix.PTRACE_EVENT_FORK && int(message) == former:
			created = held
		case cause == unix.PTRACE_EVENT_EXEC && int(message) == former:
			execed = held
		default:
			return reported, err
		}
		if own != nil && created != nil && execed != nil {
			queue = []report{*own, *created, *execed}
			if order == "exec-first" {
				queue = []report{*own, *execed, *created}
			}
		}
		return 0, nil
	}
	continueTask = func(pid, sig int) error {
		if former != 0 && pid == former {
			mu.Lock()
			line := fmt.Sprintf("continued records=%d", recorded[pid])
			mu.Unlock()
			select {
			case checked <- line:
			default:
			}
		}
		return resume(pid, sig)
	}
	go func() { _, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready); fmt.Println("run:", err) }()
	if err := <-ready; err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	for _, step := range []func() string{
		func() string { return <-checked },
		func() string { <-delivered; return "delivered" },
	} {
		done := make(chan string, 1)
		go func() { done <- step() }()
		select {
		case line := <-done:
			fmt.Println(line)
		case <-time.After(40 * time.Second):
			fmt.Println("the reports never came")
			os.Exit(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	// A control is served between reports, so the delivered ones have been handled by its answer.
	fmt.Printf("freeze: %v\n", tree.Control(ctx, "freeze"))
	c := settledCensus(tree)
	mu.Lock()
	fmt.Printf("records=%d reborn=%d bypassed=%d\n", recorded[former], tree.reborn.Load(), tree.bypassed.Load())
	mu.Unlock()
	fmt.Printf("census lost=%d %s\n", c.Lost, c.Mismatch)
	fmt.Println("stop:", tree.Stop(ctx))
	cancel()
	os.Exit(0)
}

// The first report of a task on a reused number can come before its creator's event and the EXEC
// event that would show the entry stale. It must be in the ledger before it runs, exactly once.
func TestReusedNumberIsRecordedBeforeItRuns(t *testing.T) {
	if err := exec.Command("unshare", "-Urpf", "--mount-proc", "/bin/true").Run(); err != nil {
		t.Skip("no unprivileged PID namespace here:", err)
	}
	binary, _ := os.Executable()
	fixture := histories(t)
	for _, order := range []string{"creator-first", "exec-first"} {
		t.Run(order, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "unshare", "-Urpf", "--mount-proc", binary, "tree-report-order", fixture, filepath.Join(t.TempDir(), "ready"), order).CombinedOutput()
			for _, want := range []string{"continued records=1\n", "freeze: " + ErrUnproven.Error(), "\nrecords=1 ", "census lost=0", "stop: <nil>"} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("want %q: %v: %s", want, err, out)
				}
			}
		})
	}
}

// treeCase runs one fixture in a tree with a condition set up, and prints what became of it.
func treeCase(name, fixture, marker string) {
	mode := map[string]string{"step-killed": "exec-thread", "exec-killed": "exec-thread", "pause-retry": "stuck-pause", "no-pidfds": "exec-thread", "emfile": "exec-thread", "listener": "listener-exec", "stacked": "stacked-exec"}[name]
	cmd := exec.Command("/bin/sh", "-c", `"$1" "$2" "$3" & wait; exit 7`, "sh", fixture, mode, marker)
	if name != "step-killed" && name != "exec-killed" {
		cmd = exec.Command(fixture, mode, marker)
	}
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	tree, ready := NewTree(), make(chan error, 1)
	switch name {
	case "step-killed":
		// Dead between its seccomp stop and the step: the request meets ESRCH, and the pause ends.
		original := stepTask
		stepTask = func(pid, sig int) error {
			_ = unix.Kill(threadGroup(pid), unix.SIGKILL)
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
				if err := original(pid, sig); err != nil {
					return err
				}
			}
			return unix.ETIMEDOUT
		}
	case "exec-killed":
		// Dead after de_thread and before its EXEC stop: a fatal signal skips that stop, so the event
		// never comes, the number is free, and the process's exit is reported under the leader.
		original := stepTask
		stepTask = func(pid, sig int) error {
			leader := threadGroup(pid)
			// de_thread needs the tracer to let the old leader go, so this can't wait on the tracer's thread.
			go func() {
				for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
					if unix.Kill(pid, 0) != nil {
						break
					}
				}
				_ = unix.Kill(leader, unix.SIGKILL)
			}()
			return original(pid, sig)
		}
	case "pause-retry":
		barrierPause, barrierRetry = 20*time.Millisecond, 20*time.Millisecond
	case "no-pidfds":
		pidfdOpen = func(int, int) (int, error) { return -1, unix.ENOSYS }
	case "emfile":
		original, calls := pidfdOpen, 0
		pidfdOpen = func(pid, flags int) (int, error) {
			if calls++; calls == 2 {
				return -1, unix.EMFILE
			}
			return original(pid, flags)
		}
	}
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready)
		done <- result{code, err}
	}()
	if err := <-ready; err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	if name == "step-killed" || name == "exec-killed" {
		r := <-done
		fmt.Printf("run: code=%d err=%v\n", r.code, r.err)
		os.Exit(0)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if data, _ := os.ReadFile(marker); string(data) == "exec-ready" {
			break
		}
	}
	data, _ := os.ReadFile(marker)
	refused, _ := os.ReadFile(marker + ".listener")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := tree.Control(ctx, "freeze")
	fmt.Printf("marker=%s barriers=%d retries=%d bypassed=%d listener=%s freeze=%v\n", data, tree.barriers.Load(), tree.retries.Load(), tree.bypassed.Load(), refused, err)
	fmt.Println("stop:", tree.Stop(ctx))
	cancel()
	os.Exit(0)
}

func TestExecBarrierConditions(t *testing.T) {
	binary, _ := os.Executable()
	fixture := histories(t)
	for name, want := range map[string][]string{
		"step-killed": {"run: code=7 err=<nil>"},
		"exec-killed": {"run: code=7 err=<nil>"},
		"pause-retry": {"marker=exec-ready barriers=1 ", "freeze=<nil>", "stop: <nil>"},
		"no-pidfds":   {"marker=exec-ready barriers=1 ", "freeze=<nil>", "stop: <nil>"},
		"emfile":      {"marker=exec-ready barriers=1 ", "freeze=<nil>", "stop: <nil>"},
		// Refused by the tree's filter (EPERM), not by a host that holds a listener already (EBUSY).
		"listener": {"marker=exec-ready barriers=1 ", "bypassed=0 listener=1 ", "freeze=<nil>", "stop: <nil>"},
		"stacked":  {"marker=exec-ready barriers=1 ", "bypassed=0 listener=0 ", "freeze=<nil>", "stop: <nil>"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, binary, "tree-case", name, fixture, filepath.Join(t.TempDir(), "ready")).CombinedOutput()
			for _, line := range want {
				if !strings.Contains(string(out), line) {
					t.Fatalf("want %q: %v: %s", line, err, out)
				}
			}
			if name == "pause-retry" && strings.Contains(string(out), "retries=0") {
				t.Fatalf("the pause was never given up: %s", out)
			}
		})
	}
}

// startGroup starts a fixture under a group that is stopped at the end of the test.
func startGroup(t *testing.T, fixture, mode, marker string) *Group {
	t.Helper()
	binary, _ := os.Executable()
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command(fixture, mode, marker)
	cmd.Stderr = os.Stderr
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = group.Stop(ctx)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return group
}

// Execs that fail, and one by execveat, are stepped like any other and leave the tree running.
func TestThreadExecsThatFailOrUseExecveat(t *testing.T) {
	fixture := histories(t)
	marker := filepath.Join(t.TempDir(), "ready")
	group := startGroup(t, fixture, "failing-execs", marker)
	untilWithin(t, 30*time.Second, func() bool { _, err := os.Stat(marker); return err == nil })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	before, _ := os.ReadFile(marker + ".beat")
	until(t, func() bool { after, _ := os.ReadFile(marker + ".beat"); return string(after) != string(before) })
	if err := group.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	if err := group.Thaw(ctx); err != nil {
		t.Fatal(err)
	}

	marker = filepath.Join(t.TempDir(), "ready")
	group = startGroup(t, fixture, "execveat-thread", marker)
	untilWithin(t, 30*time.Second, func() bool { data, _ := os.ReadFile(marker); return string(data) == "exec-ready" })
	if err := group.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	census, err := group.Census(ctx)
	if err != nil || len(census) != 1 || census[0].Tracked != 1 || census[0].Live != 1 {
		t.Fatalf("after an exec by a thread: %+v %v", census, err)
	}
}

// All of it together, for as long as a heavy session: every freeze acknowledges, holds, and leaves
// the tracer and the kernel agreeing on every task.
func TestFreezeHoldsThroughChaos(t *testing.T) {
	fixture := histories(t)
	marker := filepath.Join(t.TempDir(), "ready")
	group := startGroup(t, fixture, "chaos", marker)
	defer func() { _ = os.WriteFile(marker+".stop", nil, 0600) }()
	until(t, func() bool { _, err := os.Stat(marker); return err == nil })
	rounds := 200
	if testing.Short() {
		rounds = 30
	}
	for i := range rounds {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		if err := group.Freeze(ctx); err != nil {
			cancel()
			t.Fatalf("freeze %d: %v", i, err)
		}
		before, _ := os.ReadFile(marker + ".beat")
		census, err := group.Census(ctx)
		if err != nil {
			cancel()
			t.Fatalf("census %d: %v", i, err)
		}
		if len(census) != 1 || census[0].Untracked != 0 || census[0].Lost != 0 {
			cancel()
			t.Fatalf("freeze %d: tracer and kernel disagree: %+v", i, census)
		}
		time.Sleep(2 * time.Millisecond)
		if after, _ := os.ReadFile(marker + ".beat"); string(after) != string(before) {
			cancel()
			t.Fatalf("freeze %d: the tree ran", i)
		}
		err = group.Thaw(ctx)
		cancel()
		if err != nil {
			t.Fatalf("thaw %d: %v", i, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A process parked in the tree's own stop goes with a lost helper like any other.
func TestLostHelperEndsAStoppedProcess(t *testing.T) {
	binary, _ := os.Executable()
	fixture := histories(t)
	marker := filepath.Join(t.TempDir(), "ready")
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command(fixture, "self-stop", marker)
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { _, err := os.Stat(marker); return err == nil })
	raw, _ := os.ReadFile(marker + ".pid")
	pid, _ := strconv.Atoi(string(raw))
	lost := group.peers[0]
	until(t, func() bool { lost.ledger.mu.Lock(); defer lost.ledger.mu.Unlock(); return lost.ledger.records == 2 })
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := group.Freeze(ctx); err != nil {
		t.Fatal("freeze after the helper was lost:", err)
	}
	if processRuns(pid) {
		t.Fatal("a stopped process survived its proof")
	}
}

// A shell can't report what it inherited: dash clears its signal mask when it starts, and it blocks
// every signal around each vfork, so a child reading $$'s status can catch that window (the same
// goes for its descriptors around a pipe, so the shell lists those itself). cat changes nothing
// about itself, so its own view is what it was given.
const shellView = `tr '\0' ' ' < /proc/$$/cmdline; echo; echo "$FIXTURE_VALUE"; pwd; for fd in /proc/$$/fd/*; do printf '%s ' "${fd##*/}"; done; echo`

var inheritedFields = regexp.MustCompile(`(?m)^(?:Sig(?:Blk|Ign|Cgt):.*|NoNewPrivs:.*|Max .*|-?\d+$)`)

// stateOf starts cmd directly or under a group, and returns what it printed.
func stateOf(cmd *exec.Cmd, dir string, grouped bool) (string, error) {
	cmd.Dir = dir
	cmd.Env = []string{"FIXTURE_VALUE=kept", "PATH=/usr/bin:/bin"}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	var err error
	if grouped {
		binary, _ := os.Executable()
		err = NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid())).Start(cmd)
	} else {
		err = cmd.Start()
	}
	if err != nil {
		return "", err
	}
	err = cmd.Wait()
	return out.String(), err
}

// shimState prints what a command sees, started directly and under a group from this process.
func shimState(dir string) {
	var lines [2][]string
	for i, grouped := range []bool{false, true} {
		shell, err := stateOf(exec.Command("/bin/sh", "-c", shellView, "fixture-arg"), dir, grouped)
		if err != nil {
			fmt.Println("shell:", err)
			os.Exit(1)
		}
		state, err := stateOf(exec.Command("/bin/cat", "/proc/self/status", "/proc/self/limits", "/proc/self/oom_score_adj"), dir, grouped)
		if err != nil {
			fmt.Println("state:", err)
			os.Exit(1)
		}
		stat, err := stateOf(exec.Command("/bin/cat", "/proc/self/stat"), dir, grouped)
		if err != nil {
			fmt.Println("stat:", err)
			os.Exit(1)
		}
		_, after, _ := strings.Cut(stat, ") ")
		fields := strings.Fields(after)
		lines[i] = append(strings.Split(strings.TrimSpace(shell), "\n"), inheritedFields.FindAllString(state, -1)...)
		lines[i] = append(lines[i], "nice "+fields[16])
	}
	out, _ := json.Marshal(lines)
	fmt.Printf("%s\n", out)
	os.Exit(0)
}

// The shim leaves no trace in what the command sees: arguments, environment, directory, descriptors,
// signal state, priority and limits are those of a command started directly, from a parent with the
// ordinary state and from one that left everything else. no_new_privs is the one difference, on
// purpose: the seccomp filters need it, and the tree has always had it.
func TestShimIsInvisibleToTheCommand(t *testing.T) {
	kernel, err := exec.Command("uname", "-r").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Log("kernel:", strings.TrimSpace(string(kernel)))
	binary, _ := os.Executable()
	fixture := histories(t)
	for name, start := range map[string][]string{
		"own state":     {binary, "tree-shim-state", t.TempDir()},
		"changed state": {fixture, "inherit", binary, "tree-shim-state", t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := exec.Command(start[0], start[1:]...).Output()
			var states [2][]string
			if err != nil || json.Unmarshal(out, &states) != nil {
				t.Fatalf("%v: %s", err, out)
			}
			direct, grouped := states[0], states[1]
			t.Log(strings.Join(direct, "\n"))
			if len(direct) != len(grouped) {
				t.Fatalf("got %q, want %q", grouped, direct)
			}
			for i := range direct {
				if strings.HasPrefix(direct[i], "NoNewPrivs:") {
					if grouped[i] != "NoNewPrivs:\t1" {
						t.Errorf("under a group: %q", grouped[i])
					}
				} else if direct[i] != grouped[i] {
					t.Errorf("line %d: got %q, direct %q", i, grouped[i], direct[i])
				}
			}
		})
	}
}

// The shim execs the command's own argv, argv[0] included.
func TestShimKeepsArgv(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "true")
	cmd.Args[0] = "custom-name"
	throughShim(cmd)
	if cmd.Path != shimPath || strings.Join(cmd.Args, " ") != shimPath+" "+shimMarker+" /bin/sh custom-name -c true" {
		t.Fatalf("path %q, args %q", cmd.Path, cmd.Args)
	}
}

// A command that cannot be executed is a start failure, with exec's reason, as cmd.Start gave it.
func TestUnrunnableCommandIsAStartFailure(t *testing.T) {
	binary, _ := os.Executable()
	dir := t.TempDir()
	unexecutable := filepath.Join(dir, "plain")
	if err := os.WriteFile(unexecutable, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{filepath.Join(dir, "none"): "no such file", unexecutable: "permission denied"} {
		cmd := exec.Command(path)
		group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
		err := group.Start(cmd)
		if err == nil || !strings.Contains(err.Error(), ErrStart.Error()) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: got %v", path, err)
		}
		_ = cmd.Wait()
	}
}

// With nobody seizing it (a stray SIGCONT ended its stop first), the shim cannot run the command.
func TestUnseizedShimCannotExec(t *testing.T) {
	binary, _ := os.Executable()
	marker := filepath.Join(t.TempDir(), "ran")
	cmd := exec.Command("setpriv", "--no-new-privs", binary, shimMarker, "/bin/sh", "sh", "-c", `echo > "$1"`, "sh", marker)
	if err := cmd.Start(); err != nil {
		t.Skip("setpriv:", err)
	}
	until(t, func() bool {
		data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
		_, after, _ := strings.Cut(string(data), ") ")
		return strings.HasPrefix(after, "T")
	})
	if err := unix.Kill(cmd.Process.Pid, unix.SIGCONT); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != int(unix.ENOSYS) {
		t.Fatalf("got %v, want exit %d", err, int(unix.ENOSYS))
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("an unseized root ran its command")
	}
}

// signalTree reaches a process through its pidfd, and never touches a number it holds no pidfd for
// unless that number is a leader's.
func TestTeardownGoesThroughPidfds(t *testing.T) {
	victim := exec.Command("sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = victim.Process.Kill(); _ = victim.Wait() }()
	fd, err := unix.PidfdOpen(victim.Process.Pid, 0)
	if err != nil {
		t.Skip("no pidfds:", err)
	}
	bystander := exec.Command("sleep", "60")
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bystander.Process.Kill(); _ = bystander.Wait() }()
	tree := NewTree()
	tree.pidfds[victim.Process.Pid] = fd
	tree.threads[bystander.Process.Pid] = true
	tree.signalTree(map[int]int{victim.Process.Pid: -1, bystander.Process.Pid: -1})
	tree.closePidfds()
	if err := victim.Wait(); err == nil {
		t.Fatal("the process was not killed")
	}
	if !processRuns(bystander.Process.Pid) {
		t.Fatal("a thread entry was signaled by number")
	}
}

// With no pidfds a leader is signaled by number, and a thread never is, even with no leader tracked.
func TestNumericTeardownSkipsThreads(t *testing.T) {
	start := func() *exec.Cmd {
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	leader, bystander := start(), start()
	tree := NewTree()
	tree.noPidfds = true
	tree.threads[bystander.Process.Pid] = true
	tree.signalTree(map[int]int{leader.Process.Pid: -1, bystander.Process.Pid: -1})
	if err := leader.Wait(); err == nil {
		t.Fatal("the leader was not killed")
	}
	if !processRuns(bystander.Process.Pid) {
		t.Fatal("a thread's number was signaled")
	}
}

// outsideReuse is the init of a PID namespace, outside the tree. It runs the tree in a child, takes
// the number that child names, and reports whether the process holding it outlived the teardown.
func outsideReuse(binary, fixture, marker string) {
	asks, ask, _ := os.Pipe()
	answer, answers, _ := os.Pipe()
	cmd := exec.Command(binary, "tree-outside-reuse-tracer", fixture, marker)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{ask, answer}
	if err := cmd.Start(); err != nil {
		fmt.Println("tracer:", err)
		os.Exit(1)
	}
	var tid int
	if _, err := fmt.Fscan(asks, &tid); err != nil {
		fmt.Println("no number asked for:", err, cmd.Wait())
		os.Exit(1)
	}
	var taker *exec.Cmd
	for range 1000 {
		if err := os.WriteFile("/proc/sys/kernel/ns_last_pid", []byte(strconv.Itoa(tid-1)), 0600); err != nil {
			fmt.Println("aim:", err)
			os.Exit(1)
		}
		taker = exec.Command("sleep", "60")
		if err := taker.Start(); err != nil {
			fmt.Println("taker:", err)
			os.Exit(1)
		}
		if taker.Process.Pid == tid {
			break
		}
		_ = taker.Process.Kill()
		_ = taker.Wait()
		taker = nil
	}
	if taker == nil {
		fmt.Println("the number was never taken")
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(answers, tid)
	fmt.Println("tracer:", cmd.Wait())
	exited := make(chan error, 1)
	go func() { exited <- taker.Wait() }()
	select {
	case err := <-exited:
		fmt.Println("taker killed:", err)
	case <-time.After(200 * time.Millisecond):
		fmt.Println("taker alive")
	}
	os.Exit(0)
}

// outsideReuseTracer holds a thread's EXEC event back while a process outside the tree takes the
// number it freed, and has the stop request land in that window. Numeric, so no pidfds.
func outsideReuseTracer(fixture, marker string) {
	pidfdOpen = func(int, int) (int, error) { return -1, unix.ENOSYS }
	ask, answer := os.NewFile(3, "ask"), os.NewFile(4, "answer")
	cmd := exec.Command(fixture, "exec-thread", marker)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	tree, ready := NewTree(), make(chan error, 1)
	var stopped atomic.Bool
	var former, held int
	var heldStatus unix.WaitStatus
	step, wait := stepTask, waitTask
	stepTask = func(tid, sig int) error { former = tid; return step(tid, sig) }
	waitTask = func(pid int, status *unix.WaitStatus, options int, usage *unix.Rusage) (int, error) {
		if held != 0 {
			if !stopped.Load() {
				return 0, nil
			}
			reported := held
			held, *status = 0, heldStatus
			return reported, nil
		}
		reported, err := wait(pid, status, options, usage)
		if err != nil || reported == 0 || former == 0 || !status.Stopped() || status.TrapCause() != unix.PTRACE_EVENT_EXEC {
			return reported, err
		}
		// The thread's old number is free and the tree has not seen the event yet.
		_, _ = fmt.Fprintln(ask, former)
		var taken int
		if _, err := fmt.Fscan(answer, &taken); err != nil {
			fmt.Println("no answer:", err)
			os.Exit(1)
		}
		former, held, heldStatus = 0, reported, *status
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			fmt.Println("stop asked:", tree.Control(ctx, "stop"))
			stopped.Store(true)
		}()
		return 0, nil
	}
	go func() { _, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready); fmt.Println("run:", err) }()
	if err := <-ready; err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	<-tree.done
	os.Exit(0)
}

// A stop request between a thread's de_thread and its EXEC event: the number it left behind now
// belongs to a process outside the tree, and teardown must leave that process alone.
func TestTeardownSparesAnOutsiderOnAThreadsOldNumber(t *testing.T) {
	if err := exec.Command("unshare", "-Urpf", "--mount-proc", "/bin/true").Run(); err != nil {
		t.Skip("no unprivileged PID namespace here:", err)
	}
	binary, _ := os.Executable()
	fixture := histories(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unshare", "-Urpf", "--mount-proc", binary, "tree-outside-reuse", fixture, filepath.Join(t.TempDir(), "ready"))
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	for _, want := range []string{"taker alive", "run: <nil>"} {
		if err != nil || !strings.Contains(string(out), want) {
			t.Fatalf("want %q: %v: %s", want, err, out)
		}
	}
}

// protectedTree runs a tree as the persistent shell helper does: nondumpable before its first task,
// so nothing of this process can be read through /proc. BLERG_TEST_DELETE_SELF first removes this
// binary, as an upgrade does to a helper that outlives it.
func protectedTree(fixture, marker string) {
	if os.Getenv("BLERG_TEST_DELETE_SELF") == "1" {
		if err := os.Remove(os.Args[0]); err != nil {
			fmt.Println("remove:", err)
			os.Exit(1)
		}
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		fmt.Println("protect:", err)
		os.Exit(1)
	}
	cmd := exec.Command(fixture, "exec-thread", marker)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	tree, ready := NewTree(), make(chan error, 1)
	go func() { _, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready); fmt.Println("run:", err) }()
	if err := <-ready; err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if data, _ := os.ReadFile(marker); string(data) == "exec-ready" {
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := tree.Control(ctx, "freeze")
	census := tree.Control(ctx, "census")
	c := tree.census.Load()
	fmt.Printf("barriers=%d freeze=%v census=%v tracked=%d live=%d\n", tree.barriers.Load(), err, census, c.Tracked, c.Live)
	fmt.Println("stop:", tree.Stop(ctx))
	cancel()
	os.Exit(0)
}

// protectedGroup is a nondumpable daemon starting and freezing a writer.
func protectedGroup() {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		fmt.Println("protect:", err)
		os.Exit(1)
	}
	binary, err := os.Executable()
	if err != nil {
		fmt.Println("executable:", err)
		os.Exit(1)
	}
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command("sleep", "60")
	if err := group.Start(cmd); err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	fmt.Println("freeze:", group.Freeze(ctx))
	fmt.Println("thaw:", group.Thaw(ctx))
	fmt.Println("stop:", group.Stop(ctx))
	cancel()
	os.Exit(0)
}

func TestTreeUnderNondumpableTracer(t *testing.T) {
	fixture := histories(t)
	copied := filepath.Join(t.TempDir(), "binary")
	self, _ := os.Executable()
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string]string{"in place": "", "binary deleted": "BLERG_TEST_DELETE_SELF=1"} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(copied, data, 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(copied, "tree-protected", fixture, filepath.Join(t.TempDir(), "ready"))
			cmd.Env = append(os.Environ(), env)
			out, err := cmd.CombinedOutput()
			for _, want := range []string{"barriers=1 freeze=<nil> census=<nil> tracked=1 live=1", "stop: <nil>"} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("want %q: %v: %s", want, err, out)
				}
			}
		})
	}
}

func TestGroupFromNondumpableParent(t *testing.T) {
	binary, _ := os.Executable()
	out, err := exec.Command(binary, "group-protected").CombinedOutput()
	for _, want := range []string{"freeze: <nil>", "thaw: <nil>", "stop: <nil>"} {
		if err != nil || !strings.Contains(string(out), want) {
			t.Fatalf("want %q: %v: %s", want, err, out)
		}
	}
}

// exec of a binary its user cannot read leaves the process nondumpable, which same-UID PTRACE_SEIZE
// refuses (TRACEME did not care). A helper installed that way fails closed, naming the reason.
func TestExecuteOnlyBinaryFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything")
	}
	self, _ := os.Executable()
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(copied, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(copied, 0100); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	cmd := exec.Command("/bin/sh", "-c", `echo > "$1"`, "sh", marker)
	err = NewGroup(copied, uint32(os.Getuid()), uint32(os.Getgid())).Start(cmd)
	_ = cmd.Wait()
	if err == nil || !strings.Contains(err.Error(), ErrStart.Error()) {
		t.Fatalf("got %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the command ran")
	}
	t.Log(err)
}
