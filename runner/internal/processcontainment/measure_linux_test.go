//go:build linux

package processcontainment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMeasureTurnEnd prints what containment costs on a go-test-sized tree: how long a turn-end freeze
// and thaw take while a real build runs, and how long the build and a run of short execs take. It
// asserts nothing and only runs on request (BLERG_MEASURE=1), since the numbers need a quiet machine.
func TestMeasureTurnEnd(t *testing.T) {
	if os.Getenv("BLERG_MEASURE") == "" {
		t.Skip("set BLERG_MEASURE=1 to measure")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gocache := t.TempDir()
	build := func(interval time.Duration) (time.Duration, []time.Duration) {
		group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
		cmd := exec.Command("go", "build", "-a", "-o", os.DevNull, "net/http")
		cmd.Dir = gocache // std needs no module
		cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(gocache, "c"), "GOWORK=off")
		cmd.Stderr = os.Stderr
		start := time.Now()
		if err := group.Start(cmd); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var turnEnds []time.Duration
		for interval != 0 {
			select {
			case err := <-done:
				done <- err
				interval = 0
				continue
			case <-time.After(interval):
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			begin := time.Now()
			err := group.Freeze(ctx)
			if err == nil {
				err = group.Thaw(ctx)
			}
			cancel()
			if err != nil {
				t.Fatal("turn end:", err)
			}
			turnEnds = append(turnEnds, time.Since(begin))
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := group.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		return elapsed, turnEnds
	}
	execs := func() time.Duration {
		group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
		cmd := exec.Command("/bin/sh", "-c", "i=0; while [ $i -lt 1000 ]; do /bin/true; i=$((i+1)); done")
		start := time.Now()
		if err := group.Start(cmd); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	starts := func() time.Duration {
		start := time.Now()
		for range 20 {
			group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
			cmd := exec.Command("/bin/true")
			if err := group.Start(cmd); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		}
		return time.Since(start) / 20
	}
	for round := range 3 {
		plain, _ := build(0)
		frozen, turnEnds := build(500 * time.Millisecond)
		slices.Sort(turnEnds)
		fmt.Printf("round %d: build plain=%s with-turn-ends=%s turn-ends=%d p50=%s p95=%s max=%s | 1000 execs=%s | start=%s\n", round,
			plain.Round(time.Millisecond), frozen.Round(time.Millisecond), len(turnEnds),
			turnEnds[len(turnEnds)/2].Round(time.Microsecond), turnEnds[len(turnEnds)*95/100].Round(time.Microsecond), turnEnds[len(turnEnds)-1].Round(time.Microsecond),
			execs().Round(time.Millisecond), starts().Round(time.Microsecond))
	}
}

// spinCost is one helper running a fixed shell loop of execs, a heavy report rate. It prints the
// CPU the helper itself used, tracer included, tree excluded.
func spinCost(spin string, execs string) {
	d, err := time.ParseDuration(spin)
	if err != nil {
		fmt.Println("spin:", err)
		os.Exit(1)
	}
	reportSpin = d
	cmd := exec.Command("/bin/sh", "-c", "i=0; while [ $i -lt "+execs+" ]; do /bin/true; i=$((i+1)); done")
	if execs == "idle" {
		cmd = exec.Command("sleep", "5")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	tree, ready := NewTree(), make(chan error, 1)
	begin := time.Now()
	go func() { _, _ = tree.Run(cmd, cmd.Start, WriterTaskLimit, ready) }()
	if err := <-ready; err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	<-tree.done
	var usage unix.Rusage
	_ = unix.Getrusage(unix.RUSAGE_SELF, &usage)
	fmt.Println(time.Duration(usage.Utime.Nano()+usage.Stime.Nano()).Nanoseconds(), time.Since(begin).Nanoseconds())
	os.Exit(0)
}

// TestMeasureReportSpin prints what the 100 us poll costs across many busy helpers: helper CPU and
// wall time for the same work, with and without the poll. Runs on request, like TestMeasureTurnEnd.
func TestMeasureReportSpin(t *testing.T) {
	if os.Getenv("BLERG_MEASURE") == "" {
		t.Skip("set BLERG_MEASURE=1 to measure")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(helpers int, spin, execs string) (cpu, wall time.Duration) {
		var mu sync.Mutex
		var group sync.WaitGroup
		begin := time.Now()
		for range helpers {
			group.Go(func() {
				out, err := exec.Command(binary, "tree-spin-cost", spin, execs).CombinedOutput()
				if err != nil {
					t.Error(err, string(out))
					return
				}
				var used, elapsed int64
				if _, err := fmt.Sscan(string(out), &used, &elapsed); err != nil {
					t.Error(err, string(out))
					return
				}
				mu.Lock()
				defer mu.Unlock()
				cpu += time.Duration(used)
				_ = elapsed
			})
		}
		group.Wait()
		return cpu, time.Since(begin)
	}
	const execs = 500
	for _, helpers := range []int{1, 4, 16, 32, 64} {
		for round := range 2 {
			for _, spin := range []string{"0s", "100us"} {
				cpu, wall := run(helpers, spin, strconv.Itoa(execs))
				fmt.Printf("helpers=%d round=%d spin=%s: helper-cpu=%s (%.1f ms per helper-second, %s per exec) wall=%s\n", helpers, round, spin,
					cpu.Round(time.Millisecond), cpu.Seconds()*1000/(wall.Seconds()*float64(helpers)), (cpu / time.Duration(helpers*execs)).Round(time.Microsecond), wall.Round(time.Millisecond))
			}
		}
	}
	for _, helpers := range []int{1, 16} {
		for _, spin := range []string{"0s", "100us"} {
			cpu, wall := run(helpers, spin, "idle")
			fmt.Printf("idle helpers=%d spin=%s: helper-cpu=%s over %s\n", helpers, spin, cpu.Round(time.Millisecond), wall.Round(time.Millisecond))
		}
	}
}
