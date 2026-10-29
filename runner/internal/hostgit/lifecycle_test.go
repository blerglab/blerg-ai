//go:build linux || darwin

package hostgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestGroupCleanupBeforeReap(t *testing.T) {
	for _, raceExit := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "cancel racing exit"}[raceExit], func(t *testing.T) {
			cmd := exec.Command("/usr/bin/true")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			stop := make(chan struct{})
			var stages []string
			err := runCommand(stop, nil, Options{Diagnostic: func(stage string, _ time.Duration, _ error) {
				switch stage {
				case "wait selected":
					if cmd.ProcessState != nil || errors.Is(cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
						t.Error("leader reaped before group cleanup; PGID can be reused")
					}
					if raceExit {
						close(stop)
					}
				case "group kill begin", "RSS joined", "reap begin", "process joined":
					stages = append(stages, stage)
					if stage != "process joined" && cmd.ProcessState != nil {
						t.Errorf("%s after reap", stage)
					}
				}
			}}, cmd, func(_ Options, _ *exec.Cmd) *memoryWatch {
				return &memoryWatch{stop: func() {
					if cmd.ProcessState != nil {
						t.Error("supervisor stopped after reap")
					}
				}}
			})
			if raceExit && !errors.Is(err, errStopped) || !raceExit && err != nil {
				t.Fatalf("result: %v", err)
			}
			want := []string{"group kill begin", "RSS joined", "reap begin", "process joined"}
			if !reflect.DeepEqual(stages, want) {
				t.Fatalf("cleanup ordering: %q want %q", stages, want)
			}
			if cmd.ProcessState == nil {
				t.Fatal("call returned without reaping")
			}
		})
	}
}

type blockedPlainWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *blockedPlainWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}

func TestBlockedWriterIsRefusedBeforeStart(t *testing.T) {
	w := &blockedPlainWriter{entered: make(chan struct{}), release: make(chan struct{})}
	// Always release the reviewer's deliberately unsupported writer, even on the old code.
	defer close(w.release)
	cmd := exec.Command("/bin/sh", "-c", "printf x; exec /bin/sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = w
	timeout := time.NewTimer(20 * time.Millisecond)
	defer timeout.Stop()
	err := runCommand(nil, timeout.C, Options{}, cmd, platformMemoryWatch)
	if !errors.Is(err, ErrOutputWriter) || !strings.Contains(err.Error(), "output writer") || cmd.Process != nil {
		t.Fatalf("blocking writer must refuse before starting, process=%v err=%v", cmd.Process, err)
	}
	select {
	case <-w.entered:
		t.Fatal("runner invoked unsupported blocking Write")
	default:
	}
}

type blockedContextWriter struct {
	stop   chan struct{}
	active atomic.Int32
}

func (w *blockedContextWriter) WriteContext(ctx context.Context, p []byte) (int, error) {
	w.active.Add(1)
	defer w.active.Add(-1)
	close(w.stop)
	<-ctx.Done()
	return 0, ctx.Err()
}

// A context writer deliberately has a blocking Write too: the runner must use WriteContext.
func (w *blockedContextWriter) Write([]byte) (int, error) { panic("called uncancellable Write") }

func TestCancellationJoinsBlockedContextWriter(t *testing.T) {
	for _, reason := range []string{"cancel", "timeout", "memory"} {
		t.Run(reason, func(t *testing.T) {
			entered := make(chan struct{})
			w := &blockedContextWriter{stop: entered}
			stop := make(chan struct{})
			timeout := make(chan time.Time, 1)
			refused := make(chan error, 1)
			cmd := exec.Command("/bin/sh", "-c", "printf x; exec /bin/sleep 30")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout = w
			triggered := make(chan struct{})
			go func() {
				defer close(triggered)
				<-entered
				switch reason {
				case "cancel":
					close(stop)
				case "timeout":
					timeout <- time.Now()
				case "memory":
					refused <- &memoryRefusal{ErrMemoryLimit}
				}
			}()
			err := runCommand(stop, timeout, Options{}, cmd, func(Options, *exec.Cmd) *memoryWatch { return &memoryWatch{refused: refused, stop: func() {}} })
			<-triggered
			want := errStopped
			if reason == "memory" {
				want = ErrMemoryLimit
			}
			if !errors.Is(err, want) || w.active.Load() != 0 || cmd.ProcessState == nil {
				t.Fatalf("unjoined process/output: active=%d state=%v err=%v", w.active.Load(), cmd.ProcessState, err)
			}
		})
	}
}

func TestDarwinExitObservationDecision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		observed int
		state    int8
		err      error
	}{
		{"zombie", 123, darwinZombieState, nil},
		{"missing child", 0, 0, nil},
		{"observation error", 0, 0, errors.New("sysctl refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples := 0
			err := observeDarwinExit(123, func(pid int) (int, int8, error) {
				if pid != 123 {
					t.Errorf("wrong child: %d", pid)
				}
				samples++
				if samples == 1 {
					return 123, 2, nil
				}
				return tc.observed, tc.state, tc.err
			})
			if samples != 2 || (err == nil) != (tc.name == "zombie") {
				t.Fatalf("samples=%d err=%v", samples, err)
			}
		})
	}
}

func TestExitObservationRetainsLeader(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { KillGroup(cmd); _ = cmd.Wait() }()
	if err := platformObserveExit(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if cmd.ProcessState != nil || errors.Is(cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
		t.Fatal("exit observation reaped the leader")
	}
}

func TestGroupSignalReservedLeaderOnly(t *testing.T) {
	calls := 0
	signal := func(pid int, sig syscall.Signal) error {
		calls++
		if pid != -123 || sig != syscall.SIGKILL {
			t.Fatalf("wrong group signal: %d %v", pid, sig)
		}
		return nil
	}
	killGroup(nil, signal)
	killGroup(&exec.Cmd{}, signal)
	cmd := &exec.Cmd{Process: &os.Process{Pid: 123}}
	killGroup(cmd, signal)
	cmd.ProcessState = &os.ProcessState{}
	killGroup(cmd, signal)
	if calls != 1 {
		t.Fatalf("post-reap signal: %d signals", calls)
	}
}

func TestCancellationAfterExitJoinsBlockedOutput(t *testing.T) {
	entered := make(chan struct{})
	w := &blockedContextWriter{stop: entered}
	stop := make(chan struct{})
	cmd := exec.Command("/bin/sh", "-c", "printf x")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = w
	err := runCommand(stop, nil, Options{Diagnostic: func(stage string, _ time.Duration, _ error) {
		if stage == "reap begin" {
			<-entered
			close(stop)
		}
	}}, cmd, platformMemoryWatch)
	if !errors.Is(err, errStopped) || w.active.Load() != 0 || cmd.ProcessState == nil {
		t.Fatalf("output outlived reap: active=%d state=%v err=%v", w.active.Load(), cmd.ProcessState, err)
	}
}
