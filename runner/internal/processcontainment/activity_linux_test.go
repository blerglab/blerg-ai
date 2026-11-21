//go:build linux

package processcontainment

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/processtelemetry"
	"golang.org/x/sys/unix"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProcessTelemetrySleeper(t *testing.T) {
	if os.Getenv("BLERG_PROCESS_FIXTURE") == "1" {
		time.Sleep(600 * time.Millisecond)
	}
}
func TestGeneratedScriptProcessEvidence(t *testing.T) {
	// A session may itself be traced. Diagnose that permission boundary before
	// starting a helper, so nested-tracer failures are explicit rather than an
	// opaque shim exec failure.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	probe := exec.Command("sleep", "5")
	if err := probe.Start(); err != nil {
		t.Fatal(err)
	}
	if err := unix.PtraceSeize(probe.Process.Pid); err != nil {
		_ = probe.Process.Kill()
		_ = probe.Wait()
		t.Fatalf("separate tracer permission: %v", err)
	}
	_ = unix.PtraceInterrupt(probe.Process.Pid)
	var status unix.WaitStatus
	_, _ = unix.Wait4(probe.Process.Pid, &status, unix.WALL, nil)
	_ = unix.PtraceDetach(probe.Process.Pid)
	_ = probe.Process.Kill()
	_ = probe.Wait()

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "generated.sh")
	if err = os.WriteFile(path, []byte("#!/bin/sh\nexec \"$1\" -test.run=^TestProcessTelemetrySleeper$\n"), 0600); err != nil {
		t.Fatal(err)
	}
	group := NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	cmd := exec.Command("sh", path, binary)
	cmd.Env = append(os.Environ(), "BLERG_PROCESS_FIXTURE=1")
	if err = group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = group.Stop(ctx)
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var duration time.Duration
	var evidence []byte
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	completed := false
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			completed = true
		case <-deadline:
			t.Fatal("process evidence timed out")
		case <-tick.C:
			batch := group.ProcessActivity()
			for _, s := range batch.Spans {
				if s.Program == "go-test" {
					duration += s.End.Sub(s.Start)
				}
			}
			if len(batch.Spans) > 0 {
				evidence, _ = json.Marshal(batch)
			}
			if completed && duration > 300*time.Millisecond {
				if strings.Contains(string(evidence), path) || strings.Contains(string(evidence), "test.run") {
					t.Fatal("arguments retained")
				}
				t.Logf("generated shell script: go-test observed %s", duration)
				return
			}
		}
	}
}
func BenchmarkActivityPublish(b *testing.B) {
	at := time.Unix(1, 0)
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		var q *activityQueue
		if enabled {
			name = "enabled"
			q = &activityQueue{events: make(chan processtelemetry.Event, processtelemetry.QueueSize)}
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				q.publish(processtelemetry.Exec, 1, 0, at)
				if enabled && i%processtelemetry.QueueSize == processtelemetry.QueueSize-1 {
					for len(q.events) > 0 {
						<-q.events
					}
				}
			}
		})
	}
}

// Compare the exact publication path while executing real programs. This keeps
// process creation and image observation in the measurement, unlike a channel
// microbenchmark. It needs no nested tracer and runs in the session itself.
func BenchmarkActivityExec(b *testing.B) {
	order := []bool{false, true}
	if os.Getenv("BLERG_BENCH_TELEMETRY_FIRST") == "1" {
		order = []bool{true, false}
	}
	for _, enabled := range order {
		name := "disabled"
		var q *activityQueue
		if enabled {
			name = "enabled"
			tree := NewTree()
			tree.EnableActivity()
			q = tree.activity
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			at := time.Unix(1, 0)
			for i := 0; i < b.N; i++ {
				cmd := exec.Command("/bin/true")
				if err := cmd.Start(); err != nil {
					b.Fatal(err)
				}
				q.publish(processtelemetry.Exec, cmd.Process.Pid, 0, at)
				if err := cmd.Wait(); err != nil {
					b.Fatal(err)
				}
				q.publish(processtelemetry.Exit, cmd.Process.Pid, 0, at)
			}
		})
		if q != nil {
			close(q.events)
			<-q.done
		}
	}
}

// Pair real execs and alternate order to avoid attributing machine drift to
// telemetry. Report the paired difference and its normal 95% interval.
func BenchmarkActivityExecPaired(b *testing.B) {
	tree := NewTree()
	tree.EnableActivity()
	q := tree.activity
	defer func() { close(q.events); <-q.done }()
	at := time.Unix(1, 0)
	var baseline, sum, squares float64
	run := func(enabled bool) float64 {
		start := time.Now()
		cmd := exec.Command("/bin/true")
		if err := cmd.Start(); err != nil {
			b.Fatal(err)
		}
		var target *activityQueue
		if enabled {
			target = q
		}
		target.publish(processtelemetry.Exec, cmd.Process.Pid, 0, at)
		if err := cmd.Wait(); err != nil {
			b.Fatal(err)
		}
		target.publish(processtelemetry.Exit, cmd.Process.Pid, 0, at)
		return float64(time.Since(start))
	}
	for i := 0; i < b.N; i++ {
		var off, on float64
		if i%2 == 0 {
			off = run(false)
			on = run(true)
		} else {
			on = run(true)
			off = run(false)
		}
		delta := on - off
		baseline += off
		sum += delta
		squares += delta * delta
	}
	mean := sum / float64(b.N)
	scale := 100 / (baseline / float64(b.N))
	margin := 0.0
	if b.N > 1 {
		variance := math.Max(0, (squares-sum*sum/float64(b.N))/float64(b.N-1))
		margin = 1.96 * math.Sqrt(variance/float64(b.N))
	}
	b.ReportMetric(mean*scale, "exec_delta_%")
	b.ReportMetric((mean-margin)*scale, "lower95_%")
	b.ReportMetric((mean+margin)*scale, "upper95_%")
}

func TestProcessActivityLeavesSiblingEvidenceForItsOwner(t *testing.T) {
	a, b := &peer{activityIdentity: "first"}, &peer{activityIdentity: "second"}
	a.pid.Store(7)
	b.pid.Store(9)
	a.empty.Store(true)
	b.empty.Store(true)
	a.activity.Spans = []processtelemetry.Span{{Program: "go-test", Class: "tests"}}
	b.activity.Spans = []processtelemetry.Span{{Program: "vitest", Class: "tests"}}
	group := &Group{peers: []*peer{a, b}}
	wrong := group.ProcessActivityForRoots([]processtelemetry.SampleRoot{{PID: 7, Identity: "recycled"}})
	if len(wrong.Spans) != 0 {
		t.Fatal("recycled PID borrowed another root")
	}
	first := group.ProcessActivityForRoots([]processtelemetry.SampleRoot{{PID: 7, Identity: "first"}})
	second := group.ProcessActivityForRoots([]processtelemetry.SampleRoot{{PID: 9, Identity: "second"}})
	if len(first.Spans) != 1 || first.Spans[0].Program != "go-test" || first.Spans[0].Root != 7 || len(second.Spans) != 1 || second.Spans[0].Program != "vitest" || second.Spans[0].Root != 9 {
		t.Fatalf("cross-session drain: %+v %+v", first, second)
	}
}

func activityPipeFixture(t *testing.T) (*activityPipe, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	fd := int(w.Fd())
	if err = unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	return &activityPipe{fd: fd}, r
}

func worstCaseSpan() processtelemetry.Span {
	at := time.Date(2026, 11, 1, 12, 0, 0, 123456789, time.FixedZone("", -11*3600))
	return processtelemetry.Span{PID: 4194304, Root: 4194304, Branch: 4194304, CallID: "toolu_0123456789012345678901234567890123456789", Program: "golangci-lint", Class: "build_lint", Start: at, End: at, Approximate: true, Basis: "exec_observed_async_image"}
}

func TestActivityFramesFitTheAtomicPipeWrite(t *testing.T) {
	spans := make([]processtelemetry.Span, activityFrameSpans)
	for i := range spans {
		spans[i] = worstCaseSpan()
	}
	raw, err := json.Marshal(processtelemetry.Batch{Spans: spans, Dropped: math.MaxUint64})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)+1 > activityFrameBytes {
		t.Fatalf("worst frame is %d bytes, atomic limit %d", len(raw)+1, activityFrameBytes)
	}
}

func TestActivityPipeNeverBlocksOrWritesAPartialFrame(t *testing.T) {
	sink, r := activityPipeFixture(t)
	spans := make([]processtelemetry.Span, processtelemetry.MaxSpans)
	for i := range spans {
		spans[i] = worstCaseSpan()
	}
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		// Far more than a pipe holds, with nobody reading.
		for range 64 {
			sink.send(processtelemetry.Batch{Spans: spans})
		}
	}()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("telemetry blocked on a full pipe")
	}
	if sink.dropped.Load() == 0 {
		t.Fatal("a full pipe must count what it dropped")
	}
	if err := r.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var got processtelemetry.Batch
	frames := 0
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		frames++
		var b processtelemetry.Batch
		if err := json.Unmarshal(scanner.Bytes(), &b); err != nil {
			t.Fatalf("frame %d is partial: %v", frames, err)
		}
		if len(scanner.Bytes())+1 > activityFrameBytes {
			t.Fatalf("frame %d: %d bytes", frames, len(scanner.Bytes())+1)
		}
		got.Spans = append(got.Spans, b.Spans...)
	}
	if frames == 0 || len(got.Spans) == 0 {
		t.Fatal("nothing arrived")
	}
}

func TestActivityPipeCarriesTheDropCountForward(t *testing.T) {
	sink, r := activityPipeFixture(t)
	sink.dropped.Store(5)
	sink.send(processtelemetry.Batch{Dropped: 2})
	var b processtelemetry.Batch
	if err := json.NewDecoder(r).Decode(&b); err != nil {
		t.Fatal(err)
	}
	if b.Dropped != 7 || sink.dropped.Load() != 0 {
		t.Fatalf("dropped %d, left %d", b.Dropped, sink.dropped.Load())
	}
}

// Run must finish, and the empty receipt go out, however long the observer takes.
func TestClosingTheActivityQueueDoesNotWaitForTheObserver(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	q := newActivityQueue(func(int) (string, string) {
		close(entered)
		<-release
		return "", ""
	})
	q.publish(processtelemetry.Exec, 1, 0, time.Unix(1, 0))
	<-entered
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		q.close()
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("closing the queue waited on a /proc read")
	}
}

func TestFinalFlushIsBoundedWhenTheObserverStalls(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	tree := NewTree()
	tree.activity = newActivityQueue(func(int) (string, string) {
		close(entered)
		<-release
		return "", ""
	})
	tree.activity.publish(processtelemetry.Exec, 1, 0, time.Unix(1, 0))
	<-entered
	tree.activity.close()
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		tree.FlushActivity()
	}()
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("final flush waited on a stalled /proc read")
	}
}

// A peer reads frames from its own pipe, never the control stream, and keeps its bound.
func TestPeerReadsActivityFramesUpToTheBound(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	p := &peer{}
	done := make(chan struct{})
	go func() { defer close(done); p.readActivity(r) }()
	span := worstCaseSpan()
	raw, _ := json.Marshal(processtelemetry.Batch{Spans: []processtelemetry.Span{span}, Dropped: 1})
	for range processtelemetry.MaxSpans + 3 {
		if _, err = w.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	<-done
	if len(p.activity.Spans) != processtelemetry.MaxSpans || p.activity.Dropped != processtelemetry.MaxSpans+3+3 {
		t.Fatalf("%d spans, %d dropped", len(p.activity.Spans), p.activity.Dropped)
	}
}

func threadExecFixture() {
	// Holding the leader here forces the goroutine onto a sibling thread: de_thread's case.
	runtime.LockOSThread()
	done := make(chan error, 1)
	go func() {
		if unix.Gettid() == unix.Getpid() {
			done <- nil
			return
		}
		runtime.LockOSThread()
		done <- unix.Exec("/bin/sleep", []string{"sleep", "0.3"}, os.Environ())
	}()
	<-done
	os.Exit(1)
}

func TestNonLeaderExecDoesNotLeakItsFormerTID(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The test is the consumer, so it sees exactly what the tracer published.
	tree := NewTree()
	q := &activityQueue{events: make(chan processtelemetry.Event, processtelemetry.QueueSize)}
	tree.activity = q
	cmd := exec.Command(binary, "-test.run=^$")
	cmd.Env = append(os.Environ(), "BLERG_THREAD_EXEC_FIXTURE=1")
	ready := make(chan error, 1)
	finished := make(chan error, 1)
	go func() {
		_, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready)
		finished <- err
	}()
	if err = <-ready; err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("fixture did not end")
	}
	if q.dropped.Load() != 0 {
		t.Fatalf("queue dropped %d events", q.dropped.Load())
	}
	var created []int
	gone := map[int]bool{}
	for e := range q.events {
		switch e.Kind {
		case processtelemetry.Fork:
			created = append(created, e.PID)
		case processtelemetry.Exit:
			gone[e.PID] = true
		}
	}
	if len(created) == 0 {
		t.Fatal("fixture created no threads")
	}
	for _, pid := range created {
		if !gone[pid] {
			t.Errorf("task %d never left the observer: created %v, exited %v", pid, created, gone)
		}
	}
}

// BenchmarkActivityExecStorm measures the whole tracer: a shell runs 500 real execs
// under Tree.Run, with telemetry off and on in alternating order so machine drift is
// not read as telemetry cost. Run on a quiet machine, outside a session (own tracer).
func BenchmarkActivityExecStorm(b *testing.B) {
	const execs = 500
	sink, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	run := func(enabled bool) float64 {
		tree := NewTree()
		if enabled {
			tree.EnableActivity()
			pipe := &activityPipe{fd: int(sink.Fd())}
			tree.activity.sink = pipe.send
		}
		cmd := exec.Command("/bin/sh", "-c", "i=0; while [ $i -lt "+strconv.Itoa(execs)+" ]; do /bin/true; i=$((i+1)); done")
		ready, done := make(chan error, 1), make(chan error, 1)
		start := time.Now()
		go func() {
			_, err := tree.Run(cmd, cmd.Start, WriterTaskLimit, ready)
			done <- err
		}()
		if err := <-ready; err != nil {
			b.Fatal(err)
		}
		if err := <-done; err != nil {
			b.Fatal(err)
		}
		return float64(time.Since(start)) / execs
	}
	var sumOff, sum, squares float64
	for i := 0; i < b.N; i++ {
		var off, on float64
		if i%2 == 0 {
			off, on = run(false), run(true)
		} else {
			on, off = run(true), run(false)
		}
		delta := on - off
		sumOff += off
		sum += delta
		squares += delta * delta
	}
	n := float64(b.N)
	mean, scale := sum/n, 100/(sumOff/n)
	margin := 0.0
	if b.N > 1 {
		margin = 1.96 * math.Sqrt(math.Max(0, (squares-sum*sum/n)/(n-1))/n)
	}
	b.ReportMetric(sumOff/n/1e3, "off_us/exec")
	b.ReportMetric(mean/1e3, "delta_us/exec")
	b.ReportMetric(mean*scale, "delta_%")
	b.ReportMetric((mean-margin)*scale, "lower95_%")
	b.ReportMetric((mean+margin)*scale, "upper95_%")
}
