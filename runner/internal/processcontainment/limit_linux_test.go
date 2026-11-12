//go:build linux

package processcontainment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

func limitFixture(t *testing.T) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "limit")
	if out, err := exec.Command("cc", "-pthread", "-o", fixture, "testdata/limit.c").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v: %s", err, out)
	}
	return fixture
}

// treeUnderLimit runs in a child of the test (the tracer installs seccomp on its thread): the fixture
// under a limit, reporting how the tree ended and what the tracer's books say at the end.
func treeUnderLimit(fixture, mode, marker string, limit int) {
	cmd := exec.Command(fixture, mode, marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	tree, ready := NewTree(), make(chan error, 1)
	done := make(chan string, 1)
	go func() {
		code, err := tree.Run(cmd, cmd.Start, limit, ready)
		done <- fmt.Sprintf("code=%d err=%v", code, err)
	}()
	if err := <-ready; err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	storm := make(chan struct{})
	if os.Getenv("LIMIT_FREEZE_STORM") != "" {
		// Turn ends land on every state a creation can be in: at the stop, in the kernel, refused.
		go func() {
			defer close(storm)
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := os.Stat(marker + ".finished"); err == nil {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := tree.Control(ctx, "freeze"); err != nil {
					fmt.Println("freeze:", err)
				}
				if err := tree.Control(ctx, "thaw"); err != nil {
					fmt.Println("thaw:", err)
				}
				cancel()
				time.Sleep(time.Millisecond)
			}
		}()
	} else {
		close(storm)
	}
	deadline := time.Now().Add(100 * time.Second)
	for ; time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(marker + ".finished"); err == nil {
			break
		}
		select {
		case result := <-done:
			fmt.Println("ended early:", result)
			os.Exit(0)
		default:
		}
	}
	<-storm
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err := tree.Control(ctx, "census")
	cancel()
	if c := tree.census.Load(); c != nil {
		fmt.Printf("census err=%v tracked=%d reserved=%d untracked=%d lost=%d refused=%d barriers=%d\n", err, c.Tracked, c.Reserved, c.Untracked, c.Lost, tree.refused.Load(), tree.barriers.Load())
	}
	_ = os.WriteFile(marker+".census", nil, 0o600)
	fmt.Println("result:", <-done)
	os.Exit(0)
}

func runUnderLimit(t *testing.T, fixture, mode string, limit int, env ...string) (lines []string, report string) {
	t.Helper()
	binary, _ := os.Executable()
	marker := filepath.Join(t.TempDir(), "out")
	child := exec.Command(binary, "tree-under-limit", fixture, mode, marker, strconv.Itoa(limit))
	child.Env = append(os.Environ(), env...)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v: %s", err, out)
	}
	report = string(out)
	data, _ := os.ReadFile(marker)
	if !strings.Contains(report, "result: code=7 err=<nil>") || !strings.Contains(string(data), "worker status=0\n") {
		t.Fatalf("the root or its worker did not live through the limit:\n%s\n%s", report, data)
	}
	// The root is still there, with any threads it made.
	root := "[12]"
	switch mode {
	case "reserve":
		root = "81" // 64 children, 16 threads
	case "engine-fill":
		root = "95" // 78 children, 16 threads
	case "go-engine":
		root = `\d+`
	}
	if !regexp.MustCompile(`census err=<nil> tracked=` + root + ` reserved=0 untracked=0 lost=0`).MatchString(report) {
		t.Fatalf("books do not balance: %s", report)
	}
	if strings.Contains(report, "freeze:") || strings.Contains(report, "thaw:") {
		t.Fatalf("a control failed: %s", report)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n"), report
}

// With a limit of 16 the root and the worker hold 2, so every kind of creation gets exactly 14, and
// the next one fails with EAGAIN, again and again, whatever came before.
func TestLimitRefusesEveryKindOfCreationExactly(t *testing.T) {
	lines, report := runUnderLimit(t, limitFixture(t), "each-kind", 16)
	want := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "worker") {
			continue
		}
		want++
		if !regexp.MustCompile(`^(fork|vfork|clone|thread) created=14 errno=11$`).MatchString(line) {
			t.Fatalf("got %q: %v\n%s", line, lines, report)
		}
	}
	if want != 12 {
		t.Fatalf("%d results: %v", want, lines)
	}
}

// Nothing leaks a reservation: each history ends with exactly the room the limit gives.
func TestLimitAccountingLeaksNoReservation(t *testing.T) {
	fixture := limitFixture(t)
	for mode, want := range map[string]string{
		"failed-clones":    "after-failures created=30",
		"restarts":         "after-restarts created=30",
		"killed-in-clone":  "after-kills created=30",
		"exec-over-clones": "after-exec created=30",
	} {
		t.Run(mode, func(t *testing.T) {
			lines, report := runUnderLimit(t, fixture, mode, 32)
			if lines[0] != want {
				t.Fatalf("got %v, want %q\n%s", lines, want, report)
			}
		})
	}
}

// The same histories with turn ends raining on them, the exec barrier running too (non-leader execs
// with every thread at a creating stop): every count and every book still comes out exact.
func TestLimitHoldsThroughFreezes(t *testing.T) {
	fixture := limitFixture(t)
	for mode, want := range map[string]string{
		"each-kind":        "fork created=30 errno=11",
		"killed-in-clone":  "after-kills created=30",
		"exec-over-clones": "after-exec created=30",
	} {
		t.Run(mode, func(t *testing.T) {
			lines, report := runUnderLimit(t, fixture, mode, 32, "LIMIT_FREEZE_STORM=1")
			if lines[0] != want {
				t.Fatalf("got %v, want %q\n%s", lines, want, report)
			}
			if mode == "exec-over-clones" {
				counts := regexp.MustCompile(`refused=(\d+) barriers=(\d+)`).FindStringSubmatch(report)
				refused, _ := strconv.Atoi(counts[1])
				if barriers, _ := strconv.Atoi(counts[2]); refused < 40 || barriers < 5 {
					t.Fatalf("this history never reached the limit or the barrier: %s", report)
				}
			}
		})
	}
}

// A task killed at the stop, as the tracer refuses it: the failed request is its death, not the tree's.
func TestLimitRefusingADyingTaskEndsNothing(t *testing.T) {
	binary, _ := os.Executable()
	marker := filepath.Join(t.TempDir(), "out")
	out, err := exec.Command(binary, "tree-killed-at-refusal", limitFixture(t), marker).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "killed=true") || !strings.Contains(string(out), "result: code=7 err=<nil>") {
		t.Fatalf("%v: %s", err, out)
	}
}

func treeKilledAtRefusal(fixture, marker string) {
	original := refuseTask
	var killed atomic.Bool
	refuseTask = func(tid int, errno syscall.Errno) error {
		if !killed.Swap(true) {
			_ = syscall.Kill(tid, syscall.SIGKILL)
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !strings.Contains(taskStatus(tid), "State=Z") && taskStatus(tid) != "gone"; {
				if err := unix.PtraceSyscall(tid, 0); err != nil {
					break
				}
			}
		}
		return original(tid, errno)
	}
	cmd := exec.Command(fixture, "each-kind", marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	tree, ready := NewTree(), make(chan error, 1)
	done := make(chan string, 1)
	go func() {
		code, err := tree.Run(cmd, cmd.Start, 16, ready)
		done <- fmt.Sprintf("code=%d err=%v", code, err)
	}()
	if err := <-ready; err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(marker + ".finished"); err == nil {
			break
		}
	}
	_ = os.WriteFile(marker+".census", nil, 0o600)
	fmt.Println("killed="+strconv.FormatBool(killed.Load()), "result:", <-done)
	os.Exit(0)
}

// A filter of the tree's own that answers TRACE with data of its choosing decides nothing: the limit
// reads the registers.
func TestLimitIsNotFooledByTheTreesOwnTraceFilter(t *testing.T) {
	lines, report := runUnderLimit(t, limitFixture(t), "own-trace-filter", 16)
	if lines[0] != "own-filter created=14 errno=11 parent=1" {
		t.Fatalf("got %v\n%s", lines, report)
	}
}

// The root's process (the engine) keeps a reserve no descendant can reach. Here root and worker hold 2
// and the root's reader thread none, so the worker gets 14 and the root's children exactly
// rootTaskReserve. The reserve spent, its threads go on: they stop only at twice the limit plus the
// reserve, where the root alone holds a limit's worth of threads.
func TestRootKeepsItsReserveAtTheLimit(t *testing.T) {
	lines, report := runUnderLimit(t, limitFixture(t), "reserve", 16)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"worker created=14 errno=11", fmt.Sprintf("root-children created=%d errno=11", rootTaskReserve), "root-threads created=16 errno=11"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("want %q: %s\n%s", want, joined, report)
		}
	}
}

// The engine that fills the tree by itself keeps its threads too.
func TestRootThatFillsTheTreeKeepsItsThreads(t *testing.T) {
	lines, report := runUnderLimit(t, limitFixture(t), "engine-fill", 16)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"root-children created=78 errno=11", "root-threads created=16 errno=11"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("want %q: %s\n%s", want, joined, report)
		}
	}
}

// goEngine is a real Go engine at the end of its reserve: its tools refused, it starts more threads,
// and a refused one would abort it ("failed to create new OS thread").
func goEngine(marker string) {
	spawned := 0
	for {
		cmd := exec.Command("/bin/sleep", "600")
		if err := cmd.Start(); err != nil {
			if !errors.Is(err, syscall.EAGAIN) {
				fmt.Println("spawn:", err)
				os.Exit(3)
			}
			break
		}
		spawned++
	}
	var started sync.WaitGroup
	const threads = 12
	for range threads {
		started.Add(1)
		go func() {
			runtime.LockOSThread()
			started.Done()
			select {}
		}()
	}
	started.Wait()
	line := fmt.Sprintf("go-engine spawned=%d threads=%d\nworker status=0\n", spawned, threads)
	_ = os.WriteFile(marker, []byte(line), 0o600)
	_ = os.WriteFile(marker+".finished", nil, 0o600)
	for {
		if _, err := os.Stat(marker + ".census"); err == nil {
			os.Exit(7)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGoEngineLivesThroughTheEndOfItsReserve(t *testing.T) {
	binary, _ := os.Executable()
	lines, report := runUnderLimit(t, binary, "go-engine", 16)
	if !regexp.MustCompile(`^go-engine spawned=\d+ threads=12$`).MatchString(lines[0]) {
		t.Fatalf("got %v\n%s", lines, report)
	}
}

func beat(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

// A fork bomb at the real limit, through the real helper, with the root's own children spending its
// whole reserve: the engine goes on working and starting threads, the person and the agent are told, a
// turn end freezes and thaws it at full tree, and nothing is ended.
func TestForkBombDoesNotEndTheSession(t *testing.T) {
	binary, _ := os.Executable()
	fixture := limitFixture(t)
	marker := filepath.Join(t.TempDir(), "out")
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	told := make(chan string, 16)
	group.OnLimit(func(message string) { told <- message })
	cmd := exec.Command(fixture, "bomb", marker)
	cmd.Stderr = os.Stderr
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = group.Stop(ctx)
	}()
	select {
	case message := <-told:
		if message != TaskLimitMessage(WriterTaskLimit) {
			t.Fatalf("told %q", message)
		}
	case err := <-waited:
		t.Fatalf("the bomb ended the session: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("no notice within a minute")
	}
	alive := func(what string) {
		t.Helper()
		first := beat(marker + ".beat")
		deadline := time.Now().Add(20 * time.Second)
		for beat(marker+".beat") == first {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the engine stopped working", what)
			}
			select {
			case err := <-waited:
				t.Fatalf("%s: the session ended: %v", what, err)
			default:
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(marker + ".reserve-spent"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine's tools never spent the reserve (beat %s)", beat(marker+".beat"))
		}
		select {
		case err := <-waited:
			t.Fatalf("the session ended before the reserve was spent: %v", err)
		default:
		}
	}
	alive("at the end of the reserve")
	for round := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		start := time.Now()
		if err := group.Freeze(ctx); err != nil {
			cancel()
			t.Fatalf("freeze %d at the limit: %v", round, err)
		}
		held := time.Since(start)
		census, err := group.Census(ctx)
		if err != nil || len(census) != 1 {
			cancel()
			t.Fatalf("census: %v %v", census, err)
		}
		c := census[0]
		t.Logf("round %d: froze in %v, tracked=%d reserved=%d untracked=%d lost=%d", round, held, c.Tracked, c.Reserved, c.Untracked, c.Lost)
		// The engine's one step thread can show three times: going, reserved, and not yet reaped.
		if c.Tracked+c.Reserved > WriterTaskLimit+rootTaskReserve+3 || c.Untracked != 0 || c.Lost != 0 {
			cancel()
			t.Fatalf("books at the limit: %+v", c)
		}
		frozen := beat(marker + ".beat")
		time.Sleep(50 * time.Millisecond)
		if beat(marker+".beat") != frozen {
			cancel()
			t.Fatal("the engine ran during a freeze")
		}
		err = group.Thaw(ctx)
		cancel()
		if err != nil {
			t.Fatalf("thaw %d: %v", round, err)
		}
		alive("after a thaw")
	}
	if err := os.WriteFile(marker+".done", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
	case <-time.After(60 * time.Second):
		t.Fatal("the tree did not end when its engine did")
	}
}
