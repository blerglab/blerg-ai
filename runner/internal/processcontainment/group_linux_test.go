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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

	if len(os.Args) > 1 && os.Args[1] == "tree-limit" {
		cmd := exec.Command("/bin/sh", "-c", "for i in 1 2 3 4 5 6 7 8 9 10 11 12; do sleep 5 & done; wait")
		cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Setsid: true, Pdeathsig: syscall.SIGKILL}
		ready := make(chan error, 1)
		_, err := NewTree().Run(cmd, cmd.Start, 4, ready)
		switch {
		case errors.Is(err, ErrUnproven):
			fmt.Println("unproven:", err)
		case errors.Is(err, ErrTaskLimit):
			fmt.Println("task-limit")
		default:
			fmt.Println("other:", err)
		}
		os.Exit(0)
	}
	if len(os.Args) > 3 && os.Args[1] == "tree-census" {
		// Runs a tree in this child (the tracer installs seccomp on its thread), lets the fixture
		// finish its history, freezes it and compares the tracer's accounting with the kernel's.
		cmd := exec.Command(os.Args[2], os.Args[3], os.Args[4])
		cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Setsid: true, Pdeathsig: syscall.SIGKILL}
		tree, ready := NewTree(), make(chan error, 1)
		go func() { _, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready); fmt.Println("run:", err) }()
		if err := <-ready; err != nil {
			fmt.Println("start:", err)
			os.Exit(1)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(os.Args[4]); err == nil {
				break
			}
			if time.Now().After(deadline) {
				fmt.Println("fixture never became ready")
				os.Exit(1)
			}
			time.Sleep(time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := tree.Control(ctx, "freeze")
		cancel()
		// pthread_join returns once the kernel clears the thread's TID, before the tracer collects its
		// exit, so allow the last exits to arrive; a leaked entry never goes.
		tracked, live := tree.tracked.Load(), tracedByMe()
		for settle := time.Now().Add(2 * time.Second); tracked != int64(live) && time.Now().Before(settle); {
			time.Sleep(10 * time.Millisecond)
			tracked, live = tree.tracked.Load(), tracedByMe()
		}
		fmt.Printf("freeze: %v\ncensus tracked=%d live=%d\n", err, tracked, live)
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		fmt.Println("stop:", tree.Stop(ctx))
		cancel()
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "initial-siginfo" {
		fmt.Println(initialSiginfoProbe())
		os.Exit(0)
	}
	if len(os.Args) > 4 && os.Args[1] == "ledger-fail" {
		// Fails the ledger's Nth record (or reports no pidfd support) under a tree whose root writes
		// one marker and whose forked subshell writes another.
		failAt, _ := strconv.Atoi(os.Args[3])
		cmd := exec.Command("/bin/sh", "-c", `echo > "$1"; (echo > "$2"); true`, "sh", os.Args[4], os.Args[5])
		cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Setsid: true, Pdeathsig: syscall.SIGKILL}
		tree, ready, calls := NewTree(), make(chan error, 1), 0
		tree.Ledger = func(int) error {
			calls++
			if calls != failAt {
				return nil
			}
			if os.Args[2] == "enosys" {
				return unix.ENOSYS
			}
			return errors.New("injected record failure")
		}
		_, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready)
		fmt.Printf("run: %v\ncalls: %d\n", err, calls)
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[1] == "tree-pidfd-errno" {
		// Runs a threaded root with a ledger; a non-leader's pidfd_open fails with the named errno, as
		// it does on that kernel generation (EINVAL before Linux 6.16, ENOENT since).
		refusal := map[string]error{"einval": unix.EINVAL, "enoent": unix.ENOENT}[os.Args[2]]
		original := pidfdOpen
		pidfdOpen = func(pid, flags int) (int, error) {
			fd, err := original(pid, flags)
			if errors.Is(err, unix.EINVAL) {
				err = refusal
			}
			return fd, err
		}
		cmd := exec.Command(os.Args[0], "many-threads")
		cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Setsid: true, Pdeathsig: syscall.SIGKILL}
		tree, ready, records := NewTree(), make(chan error, 1), 0
		tree.Ledger = func(int) error { records++; return nil }
		code, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready)
		fmt.Printf("run: code=%d err=%v records=%d\n", code, err, records)
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "ledger-nofile" {
		fmt.Println(ledgerWithoutDescriptors())
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "many-threads" {
		// More OS threads than the old 128-task limit, as a parallel build on a many-core host has.
		var wg sync.WaitGroup
		for range 300 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				runtime.LockOSThread()
				time.Sleep(300 * time.Millisecond)
			}()
		}
		wg.Wait()
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[1] == "spawner" {
		// Starts short processes back to back (os/exec uses vfork), writing a heartbeat between them,
		// as a Go build or test run does.
		deadline := time.Now().Add(60 * time.Second)
		for i := 0; time.Now().Before(deadline); i++ {
			_ = exec.Command("/bin/true").Run()
			if i%20 == 0 {
				_ = os.WriteFile(os.Args[2], []byte(strconv.Itoa(i)), 0600)
			}
		}
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[1] == "tree-killed-in-stop" {
		// SIGKILLs a non-root task the moment before the tracer continues it from a stop (in the event
		// loop, or in the thaw after a freeze), so the continue always meets a task already dying.
		cmd := exec.Command("/bin/sh", "-c", "/bin/sleep 1 & wait; exit 7")
		cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Setsid: true, Pdeathsig: syscall.SIGKILL}
		tree, ready := NewTree(), make(chan error, 1)
		var armed atomic.Bool
		armed.Store(os.Args[2] == "event")
		killed, refused := 0, error(nil)
		original := continueTask
		continueTask = func(pid, sig int) error {
			if armed.Load() && killed == 0 && pid != cmd.Process.Pid {
				killed = pid
				_ = unix.Kill(pid, unix.SIGKILL)
				refused = original(pid, sig)
				return refused
			}
			return original(pid, sig)
		}
		done := make(chan string, 1)
		go func() {
			code, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready)
			done <- fmt.Sprintf("code=%d err=%v", code, err)
		}()
		if err := <-ready; err != nil {
			fmt.Println("start:", err)
			os.Exit(1)
		}
		if os.Args[2] == "thaw" {
			time.Sleep(200 * time.Millisecond) // sleep is running
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := tree.Control(ctx, "freeze")
			armed.Store(true)
			if err == nil {
				err = tree.Control(ctx, "thaw")
			}
			cancel()
			fmt.Println("freeze/thaw:", err)
		}
		fmt.Println("run:", <-done)
		fmt.Printf("killed=%t continue=%v\n", killed != 0, refused)
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[1] == "tree-killed-at-event" {
		// SIGKILLs a non-root task at its first FORK (or EXEC) event stop and lets it reach its EXIT
		// stop before the tracer reads the event's message, which then holds an exit status.
		event := map[string]int{"fork": unix.PTRACE_EVENT_FORK, "exec": unix.PTRACE_EVENT_EXEC}[os.Args[2]]
		cmd := exec.Command("/bin/sh", "-c", `(/bin/sh -c "/bin/sleep 0.3 & wait"); exit 7`)
		cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Setsid: true, Pdeathsig: syscall.SIGKILL}
		tree, ready := NewTree(), make(chan error, 1)
		killed, inExit := false, false
		original := readEventMessage
		readEventMessage = func(pid int) (uint, error) {
			if code, err := stopCode(pid); !killed && err == nil && code == int32(unix.SIGTRAP)|int32(event)<<8 && pid != cmd.Process.Pid {
				killed = true
				_ = unix.Kill(pid, unix.SIGKILL)
				for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !inExit; time.Sleep(time.Millisecond) {
					code, err = stopCode(pid)
					inExit = err == nil && code == int32(unix.SIGTRAP)|unix.PTRACE_EVENT_EXIT<<8
				}
			}
			return original(pid)
		}
		code, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready)
		fmt.Printf("run: code=%d err=%v\nkilled=%t in-exit=%t\n", code, err, killed, inExit)
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[1] == "group-killer" {
		// Kills its own background shells as an engine does when a tool times out or a monitor
		// expires: each pipeline gets its own process group, and the whole group is sent SIGKILL
		// while its tasks are busy reporting to the tracer (spawning, exiting, catching SIGCHLD).
		deadline := time.Now().Add(60 * time.Second)
		for i := 0; time.Now().Before(deadline); i++ {
			cmd := exec.Command("/bin/sh", "-c", "while :; do /bin/true | /bin/cat; done | /bin/cat")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if cmd.Start() == nil {
				time.Sleep(time.Duration(i%7) * time.Millisecond)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Wait()
			}
			_ = os.WriteFile(os.Args[2], []byte(strconv.Itoa(i)), 0600)
		}
		os.Exit(0)
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

// tracedByMe counts the kernel tasks (threads) this process traces. TracerPid names the tracing
// thread, so match any thread of this process.
func tracedByMe() int {
	tracers := map[string]bool{}
	threads, _ := filepath.Glob("/proc/self/task/[0-9]*")
	for _, thread := range threads {
		tracers["TracerPid:\t"+filepath.Base(thread)+"\n"] = true
	}
	statuses, _ := filepath.Glob("/proc/[0-9]*/task/[0-9]*/status")
	n := 0
	for _, path := range statuses {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for line := range strings.Lines(string(data)) {
			if tracers[line] {
				n++
				break
			}
		}
	}
	return n
}
func until(t *testing.T, f func() bool) {
	t.Helper()
	untilWithin(t, 5*time.Second, f)
}
func untilWithin(t *testing.T, limit time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
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

// A lost helper's tree is proven gone by its pidfd ledger, never by the helper's absence alone:
// the processes it ran, including a detached one, are gone once freeze and stop succeed.
func TestGroupLostHelperProvenGoneByLedger(t *testing.T) {
	for _, frozen := range []bool{true, false} {
		t.Run(fmt.Sprintf("frozen=%t", frozen), func(t *testing.T) {
			dir := t.TempDir()
			pids, beat := filepath.Join(dir, "pids"), filepath.Join(dir, "beat")
			binary, _ := os.Executable()
			group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
			cmd := exec.Command("sh", "-c", `setsid sh -c 'echo $$ >> "$1"; while :; do echo x >> "$2"; sleep 0.005; done' sh "$1" "$2" & echo $$ >> "$1"; exec sleep 60`, "sh", pids, beat)
			if err := group.Start(cmd); err != nil {
				t.Fatal(err)
			}
			until(t, func() bool { data, _ := os.ReadFile(pids); return strings.Count(string(data), "\n") == 2 })
			// Every process of the tree was recorded before it ran (the record may still be queued).
			lost := group.peers[0]
			until(t, func() bool {
				recorded := map[string]bool{}
				lost.ledger.mu.Lock()
				for _, fd := range lost.ledger.fds {
					info, _ := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
					for line := range strings.Lines(string(info)) {
						if pid, ok := strings.CutPrefix(line, "Pid:"); ok {
							recorded[strings.TrimSpace(pid)] = true
						}
					}
				}
				lost.ledger.mu.Unlock()
				data, _ := os.ReadFile(pids)
				for _, pid := range strings.Fields(string(data)) {
					if !recorded[pid] {
						return false
					}
				}
				return true
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if frozen {
				if err := group.Freeze(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.Process.Kill(); err != nil { // the helper
				t.Fatal(err)
			}
			_ = cmd.Wait()
			if err := group.Freeze(ctx); err != nil {
				t.Fatal("freeze after the helper was lost:", err)
			}
			data, _ := os.ReadFile(pids)
			for _, field := range strings.Fields(string(data)) {
				pid, _ := strconv.Atoi(field)
				if processRuns(pid) {
					t.Fatalf("process %d survived its lost helper's proof", pid)
				}
			}
			before, _ := os.Stat(beat)
			time.Sleep(30 * time.Millisecond)
			if after, _ := os.Stat(beat); after.Size() != before.Size() {
				t.Fatal("the lost helper's writer kept writing")
			}
			if err := group.Thaw(ctx); err != nil {
				t.Fatal(err)
			}
			// The session goes on: a fresh helper is admitted and runs the next command.
			next := exec.Command("true")
			if err := group.Start(next); err != nil {
				t.Fatal("start after the lost helper was proven gone:", err)
			}
			if err := next.Wait(); err != nil {
				t.Fatal("command after the lost helper:", err)
			}
			if err := group.Stop(ctx); err != nil {
				t.Fatal("stop after the helper was lost:", err)
			}
		})
	}
}

// The proof waits for every recorded process. Until then freeze and stop fail closed, yet thaw
// reopens admission: a dead helper never holds the session closed.
func TestGroupLostHelperWaitsForEveryRecordedProcess(t *testing.T) {
	survivor := exec.Command("sleep", "60")
	if err := survivor.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = survivor.Process.Kill(); _ = survivor.Wait() }()
	binary, _ := os.Executable()
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command("sleep", "60")
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	// Stand-in for a recorded process that outlives its tracer (a killed task stuck in I/O).
	fd, err := unix.PidfdOpen(survivor.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	lost := group.peers[0]
	lost.ledger.mu.Lock()
	lost.ledger.fds = append(lost.ledger.fds, fd)
	lost.ledger.mu.Unlock()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	<-lost.ended
	short, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	err = group.Freeze(short)
	cancel()
	if !errors.Is(err, ErrUnproven) || !strings.Contains(err.Error(), "not yet proven gone") {
		t.Fatalf("freeze proved a tree with a live recorded process: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := group.Thaw(ctx); err != nil {
		t.Fatal("thaw held admission closed for a dead helper:", err)
	}
	next := exec.Command("sleep", "60")
	if err := group.Start(next); err != nil {
		t.Fatal("next engine refused after a lost helper:", err)
	}
	defer func() { _ = next.Process.Kill(); _ = next.Wait() }()
	_ = survivor.Process.Kill()
	_ = survivor.Wait()
	if err := group.Freeze(ctx); err != nil {
		t.Fatal("freeze once every recorded process exited:", err)
	}
	if err := group.Thaw(ctx); err != nil {
		t.Fatal(err)
	}
	if err := group.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// A helper that kept no ledger (pidfds unavailable) never has its lost tree proven gone.
func TestGroupLostHelperWithoutLedgerFailsClosed(t *testing.T) {
	binary, _ := os.Executable()
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command("sleep", "60")
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	lost := group.peers[0]
	until(t, func() bool { lost.ledger.mu.Lock(); defer lost.ledger.mu.Unlock(); return lost.ledger.records == 1 })
	lost.ledger.mu.Lock()
	lost.ledger.records = 0 // as if the helper had found no pidfd support
	lost.ledger.mu.Unlock()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	<-lost.ended
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := group.Stop(ctx); !errors.Is(err, ErrUnproven) {
		t.Fatalf("a lost helper without a ledger was proven gone: %v", err)
	}
}

// The tracer tells a new task's first stop from its own freeze stops by siginfo; check that on the
// running kernel.
func TestInitialStopSiginfoOnThisKernel(t *testing.T) {
	binary, _ := os.Executable()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "initial-siginfo").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "initial=true tkill=false" {
		t.Fatalf("auto-attach stop not recognized, or a freeze stop mistaken for one: %v: %s", err, out)
	}
}

// initialSiginfoProbe traces a shell, takes its forked child's auto-attach stop, then a tkill stop.
func initialSiginfoProbe() string {
	runtime.LockOSThread()
	cmd := exec.Command("/bin/sh", "-c", "sleep 2 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return err.Error()
	}
	root := cmd.Process.Pid
	defer func() { _ = unix.Kill(root, unix.SIGKILL) }()
	var status unix.WaitStatus
	if _, err := unix.Wait4(root, &status, 0, nil); err != nil {
		return err.Error()
	}
	if err := unix.PtraceSetOptions(root, unix.PTRACE_O_TRACEFORK|unix.PTRACE_O_TRACEVFORK|unix.PTRACE_O_TRACECLONE|unix.PTRACE_O_EXITKILL); err != nil {
		return err.Error()
	}
	_ = unix.PtraceCont(root, 0)
	child, result := 0, ""
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		pid, err := unix.Wait4(-1, &status, unix.WALL, nil)
		if err != nil {
			return err.Error()
		}
		if !status.Stopped() {
			continue
		}
		if pid != root && status.StopSignal() == unix.SIGSTOP { // event stops carry SIGTRAP
			if child == 0 {
				child = pid
				result = fmt.Sprintf("initial=%t", initialStop(pid))
				_ = unix.PtraceCont(pid, 0)
				_, _, _ = unix.Syscall(unix.SYS_TKILL, uintptr(pid), uintptr(unix.SIGSTOP), 0)
				continue
			}
			if pid == child {
				result += fmt.Sprintf(" tkill=%t", initialStop(pid))
				_ = unix.Kill(child, unix.SIGKILL)
				return result
			}
		}
		_ = unix.PtraceCont(pid, 0)
	}
	return "timed out: " + result
}

// A record the tracer could not send keeps its process from ever running; no pidfd support at
// the root runs the tree without a ledger (which then never proves a lost helper's tree gone).
func TestTreeLedgerFailures(t *testing.T) {
	binary, _ := os.Executable()
	for _, c := range []struct {
		mode, at      string
		first, second bool
		run           string
	}{
		{"fail", "1", false, false, "run: process containment could not be proven"},
		{"fail", "2", true, false, ""},
		{"enosys", "1", true, true, "calls: 1"},
	} {
		t.Run(c.mode+"-"+c.at, func(t *testing.T) {
			dir := t.TempDir()
			first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
			out, err := exec.Command(binary, "ledger-fail", c.mode, c.at, first, second).CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if !strings.Contains(string(out), c.run) {
				t.Fatalf("want %q in %s", c.run, out)
			}
			for path, want := range map[string]bool{first: c.first, second: c.second} {
				if _, err := os.Stat(path); (err == nil) != want {
					t.Fatalf("%s ran=%t, want %t: %s", filepath.Base(path), err == nil, want, out)
				}
			}
		})
	}
}

// pidfd_open refuses a thread that isn't its group's leader, with EINVAL before Linux 6.16 and ENOENT
// since. A thread's process is already recorded, so neither may end the tree: the tracer ended every
// multi-threaded command at its first thread on 6.16 and later, without saying why.
func TestTreeRecordsNoThreadOnEveryKernelGeneration(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, refusal := range []string{"einval", "enoent"} {
		t.Run(refusal, func(t *testing.T) {
			var stderr strings.Builder
			child := exec.Command(binary, "tree-pidfd-errno", refusal)
			child.Stderr = &stderr
			out, err := child.Output()
			if err != nil {
				t.Fatalf("child: %v: %s%s", err, out, stderr.String())
			}
			// One record, the root's: its threads are not processes.
			if got := string(out); !strings.Contains(got, "run: code=0 err=<nil> records=1") {
				t.Fatalf("the threaded tree was ended: %s%s", got, stderr.String())
			}
		})
	}
}

// Whatever ends a tree early says why on the helper's stderr; a silent exit status 137 is unexplainable.
func TestTreeEndedByALedgerFailureSaysWhy(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var stderr strings.Builder
	child := exec.Command(binary, "ledger-fail", "fail", "2", filepath.Join(dir, "first"), filepath.Join(dir, "second"))
	child.Stderr = &stderr
	if out, err := child.Output(); err != nil {
		t.Fatalf("child: %v: %s%s", err, out, stderr.String())
	}
	if !strings.Contains(stderr.String(), "injected record failure; ending the tree") {
		t.Fatalf("the tree was ended without a reason: %q", stderr.String())
	}
}

// A record that arrives truncated breaks the ledger for good.
func TestLedgerTruncatedRecordBreaksProof(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(pair[1]) }()
	parent := os.NewFile(uintptr(pair[0]), "parent")
	defer func() { _ = parent.Close() }()
	raw, err := parent.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var ledger pidLedger
	ledger.pruneAt = 64
	reader := &streamReader{raw: raw, ledger: &ledger, oob: make([]byte, unix.CmsgSpace(4))}
	if err := unix.Sendmsg(pair[1], []byte{'\n'}, unix.UnixRights(0, 1, 2), nil, 0); err != nil { // CmsgSpace(4) rounds up to room for two
		t.Fatal(err)
	}
	if _, err := reader.Read(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	ledger.close()
	if !strings.Contains(ledger.broken, "truncated") {
		t.Fatalf("a truncated record left the ledger trusted: %q", ledger.broken)
	}
}

// With no descriptor left, the kernel drops a record and says so; the ledger is then broken.
func TestLedgerDescriptorExhaustionBreaksProof(t *testing.T) {
	binary, _ := os.Executable()
	out, err := exec.Command(binary, "ledger-nofile").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "broken: control message truncated") {
		t.Fatalf("a record dropped for lack of descriptors left the ledger trusted: %v: %s", err, out)
	}
}

func ledgerWithoutDescriptors() string {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err.Error()
	}
	parent := os.NewFile(uintptr(pair[0]), "parent")
	raw, err := parent.SyscallConn()
	if err != nil {
		return err.Error()
	}
	var ledger pidLedger
	ledger.pruneAt = 1 << 30
	reader := &streamReader{raw: raw, ledger: &ledger, oob: make([]byte, unix.CmsgSpace(16*4))}
	if err := unix.Sendmsg(pair[1], []byte{'\n'}, unix.UnixRights(pair[1]), nil, 0); err != nil {
		return err.Error()
	}
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return err.Error()
	}
	// Fill every free descriptor below a small soft limit.
	limit.Cur = 256
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return err.Error()
	}
	for {
		if _, err := unix.Dup(0); err != nil {
			break
		}
	}
	if _, err := reader.Read(make([]byte, 16)); err != nil {
		return err.Error()
	}
	return "broken: " + ledger.broken
}

// processRuns reports whether pid names a process that has not exited (a zombie has).
func processRuns(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	end := strings.LastIndex(string(raw), ")")
	return end < 0 || !strings.HasPrefix(string(raw)[end+1:], " Z") && !strings.HasPrefix(string(raw)[end+1:], " X")
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
	cmd.Stderr = os.Stderr // the tracer's own diagnostics
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

func TestGroupCommandWithManyThreadsSurvives(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command(binary, "many-threads")
	cmd.Stderr = os.Stderr // the tracer's own diagnostics
	if err = group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatalf("a 300-thread command was ended under the writer limit %d: %v", WriterTaskLimit, err)
	}
}

func TestTreeOverTaskLimitReportsReasonAndEnds(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The tracer installs a seccomp filter on its thread: run it in a child, never in this process.
	start := time.Now()
	var stderr strings.Builder
	child := exec.Command(binary, "tree-limit")
	child.Stderr = &stderr
	out, err := child.Output()
	if err != nil {
		t.Fatalf("tree-limit child: %v: %s%s", err, out, stderr.String())
	}
	// The tracer says why, with its count and the kernel's, so a runaway tree is told from accounting.
	if !strings.Contains(stderr.String(), "task limit 4 passed (tracked=5 live=5)") {
		t.Fatalf("limit kill not reported with counts: %q", stderr.String())
	}
	if got := strings.TrimSpace(string(out)); got != "task-limit" {
		t.Fatalf("over the limit: got %q, want a proven-empty ErrTaskLimit end", got)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("the tree was not ended when it passed its limit")
	}
	if msg := TaskLimitMessage(4, "shell"); !strings.Contains(msg, "process limit reached") || !strings.Contains(msg, "this shell") {
		t.Fatalf("message: %q", msg)
	}
}

// A freeze that lands while a task is blocked in vfork (its child not yet exec'd) completes: the
// blocked parent runs no code of its own while its child is stopped. Before, such a freeze never
// completed and the session was left frozen ("writer admission closed").
func TestGroupFreezesASessionThatKeepsSpawning(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	beat := filepath.Join(t.TempDir(), "beat")
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command(binary, "spawner", beat)
	cmd.Stderr = os.Stderr // the tracer's own diagnostics
	if err = group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = group.Stop(ctx)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	until(t, func() bool { data, err := os.ReadFile(beat); return err == nil && len(data) > 0 })
	for i := range 150 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := group.Freeze(ctx)
		cancel()
		if err != nil {
			t.Fatalf("freeze %d of a spawning session: %v", i, err)
		}
		before, _ := os.ReadFile(beat)
		time.Sleep(40 * time.Millisecond)
		if after, _ := os.ReadFile(beat); string(after) != string(before) {
			t.Fatalf("freeze %d: the session kept running (%s -> %s)", i, before, after)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		err = group.Thaw(ctx)
		cancel()
		if err != nil {
			t.Fatalf("thaw %d: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}
}

// Respawns share the writer group: a helper is retired by its empty receipt or, if it was lost, by
// its ledger's proof; freeze and thaw then reach only live helpers.
func TestGroupFreezeAfterEngineRespawn(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-helper=%t", lost), func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
			old := exec.Command("sleep", "60")
			if err := group.Start(old); err != nil {
				t.Fatal(err)
			}
			if lost {
				if err := old.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				err := group.peers[0].call(ctx, "stop")
				cancel()
				if err != nil {
					t.Fatal(err)
				}
			}
			_ = old.Wait()
			<-group.peers[0].ended
			live := exec.Command("sleep", "60")
			if err := group.Start(live); err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = group.Stop(ctx)
				_ = live.Process.Kill()
				_ = live.Wait()
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := group.Freeze(ctx); err != nil {
				t.Fatal("freeze after respawn:", err)
			}
			if err := group.Thaw(ctx); err != nil {
				t.Fatal("thaw after respawn:", err)
			}
			// The retired helper (receipt or ledger proof) is dropped at the next admission; only the
			// live helper is ever sent a frame.
			again := exec.Command("sleep", "60")
			if err := group.Start(again); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = again.Process.Kill(); _ = again.Wait() }()
			if len(group.peers) != 2 || group.peers[0].pid.Load() != int64(live.Process.Pid) {
				t.Fatalf("retired helper retained: %d peers", len(group.peers))
			}
			if err := group.Freeze(ctx); err != nil {
				t.Fatal("freeze with two live helpers:", err)
			}
		})
	}
}

// An engine that SIGKILLs its own background shells (a tool timeout, an expired monitor) must
// survive: a task killed while the tracer holds it in a stop is dying, not escaping, and must not
// make the tracer end the whole tree. Before, the engine was killed (exit status 137) with no
// reason given.
func TestGroupSurvivesEngineKillingItsOwnShells(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	beat := filepath.Join(t.TempDir(), "beat")
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command(binary, "group-killer", beat)
	cmd.Stderr = os.Stderr // the tracer's own diagnostics
	if err = group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = group.Stop(ctx)
		_ = cmd.Process.Kill()
	}()
	deadline := time.Now().Add(20 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		select {
		case err := <-exited:
			last, _ := os.ReadFile(beat)
			t.Fatalf("the engine was ended after %s shells: %v", last, err)
		default:
		}
		// Turn ends land while shells are being killed, as they do in a session.
		if i%10 == 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := group.Freeze(ctx); err != nil {
				cancel()
				t.Fatalf("freeze %d: %v", i, err)
			}
			err := group.Thaw(ctx)
			cancel()
			if err != nil {
				t.Fatalf("thaw %d: %v", i, err)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if last, _ := os.ReadFile(beat); len(last) == 0 {
		t.Fatal("the engine never killed a shell")
	}
}

// Deterministic form of the race above: the task the tracer is about to continue is SIGKILLed
// first, so the continue fails with ESRCH. The tree goes on and the root's own exit code is kept;
// before, the tracer ended the whole tree and the root died of SIGKILL (137).
func TestTreeContinuesPastATaskKilledInAStop(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, where := range []string{"event", "thaw"} {
		t.Run(where, func(t *testing.T) {
			var stderr strings.Builder
			child := exec.Command(binary, "tree-killed-in-stop", where)
			child.Stderr = &stderr
			out, err := child.Output()
			if err != nil {
				t.Fatalf("child: %v: %s%s", err, out, stderr.String())
			}
			got := string(out)
			if !strings.Contains(got, "killed=true continue=no such process") {
				t.Fatalf("the continue did not meet a killed task: %s", got)
			}
			if where == "thaw" && !strings.Contains(got, "freeze/thaw: <nil>") {
				t.Fatalf("thaw failed over a dying task: %s%s", got, stderr.String())
			}
			if !strings.Contains(got, "run: code=7 err=<nil>") {
				t.Fatalf("the tree was ended instead of running on: %s%s", got, stderr.String())
			}
		})
	}
}

// A task killed at its creation (or exec) event goes on into its EXIT stop before the tracer reads
// the event's message, so the read returns an exit status (9 for SIGKILL) instead of a TID. Before,
// the tracer tracked that bogus TID forever: every later freeze timed out and the tree end was
// reported unproven (and an exec's bogus former TID could delete a live entry).
func TestTreeIgnoresAnEventMessageFromAnExitStop(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"fork", "exec"} {
		t.Run(event, func(t *testing.T) {
			var stderr strings.Builder
			child := exec.Command(binary, "tree-killed-at-event", event)
			child.Stderr = &stderr
			out, err := child.Output()
			if err != nil {
				t.Fatalf("child: %v: %s%s", err, out, stderr.String())
			}
			got := string(out)
			if !strings.Contains(got, "killed=true in-exit=true") {
				t.Fatalf("the task did not reach its EXIT stop before the read: %s", got)
			}
			if !strings.Contains(got, "run: code=7 err=<nil>") {
				t.Fatalf("the tree's accounting broke: %s%s", got, stderr.String())
			}
		})
	}
}
