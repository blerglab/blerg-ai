//go:build darwin || linux

package hostgit

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestDarwinDiagnosticJoinsBlockedSampleAndProcess(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	defer KillGroup(cmd)
	stop := make(chan struct{})
	var mu sync.Mutex
	var stages []string
	o := Options{Diagnostic: func(stage string, elapsed time.Duration, _ error) {
		mu.Lock()
		defer mu.Unlock()
		if elapsed < 0 {
			t.Error("negative timing")
		}
		stages = append(stages, stage)
	}}
	err := runCommand(stop, nil, o, cmd, func(o Options, cmd *exec.Cmd) *memoryWatch {
		return watchGroupMemoryObserved(cmd, 256<<10, func(ctx context.Context, _ int) (uint64, error) {
			close(stop)
			<-ctx.Done()
			return 0, ctx.Err()
		}, o.Diagnostic)
	})
	if !errors.Is(err, errStopped) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, stage := range []string{"start begin", "start", "wait begin", "wait interrupted", "group kill begin", "killed process joined", "RSS first sample begin", "RSS sample", "RSS supervisor stopped", "RSS join begin", "RSS joined"} {
		found := false
		for _, recorded := range stages {
			found = found || stage == recorded
		}
		if !found {
			t.Errorf("missing lifecycle diagnostic %q in %q", stage, stages)
		}
	}
}

func TestDarwinMemoryWatchDecision(t *testing.T) {
	for _, rss := range []uint64{0, (256 << 10) - 1, 256 << 10, (256 << 10) + 1} {
		cmd := &exec.Cmd{Process: &os.Process{Pid: 12345}}
		observed := make(chan struct{}, 1)
		watch := watchGroupMemory(cmd, 256<<10, func(_ context.Context, pgid int) (uint64, error) {
			if pgid != 12345 {
				t.Errorf("watched wrong group: %d", pgid)
			}
			select {
			case observed <- struct{}{}:
			default:
			}
			return rss, nil
		})
		<-observed
		if rss > 256<<10 {
			select {
			case err := <-watch.refused:
				if !errors.Is(err, ErrMemoryLimit) {
					t.Errorf("wrong refusal: %v", err)
				}
			case <-time.After(time.Second):
				t.Error("over-budget group was not refused")
			}
		} else {
			select {
			case err := <-watch.refused:
				t.Errorf("in-budget group refused: %v", err)
			case <-time.After(2 * memoryPollInterval):
			}
		}
		watch.stop()
	}
	if memoryLimitKB(Options{}) != MemoryLimitKB || memoryLimitKB(Options{LimitKB: 256 << 10}) != 256<<10 {
		t.Fatal("worker budget was changed")
	}
}

// Exercise the actual wait/refusal/kill lifecycle on Linux with the Darwin sampler seam. The
// helper joins the leader's process group but is our direct child, so both kills can be reaped
// and verified without relying on init's handling of orphaned zombies.
func TestDarwinMemoryRefusalKillsGroup(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "overrun", true: "unavailable"}[unavailable], func(t *testing.T) {
			leader := exec.Command("/bin/sleep", "30")
			leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			defer KillGroup(leader)
			var helper *exec.Cmd
			var joined chan error
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			err := runCommand(nil, deadline.C, Options{MemoryLimit: true, LimitKB: 256 << 10}, leader, func(o Options, cmd *exec.Cmd) *memoryWatch {
				helper = exec.Command("/bin/sleep", "30")
				helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: cmd.Process.Pid}
				if err := helper.Start(); err != nil {
					t.Fatal(err)
				}
				joined = make(chan error, 1)
				go func() { joined <- helper.Wait() }()
				return watchGroupMemory(cmd, memoryLimitKB(o), func(ctx context.Context, pgid int) (uint64, error) {
					if pgid != cmd.Process.Pid {
						t.Errorf("watching wrong group: %d", pgid)
					}
					if unavailable {
						return 0, errors.New("injected sample failure")
					}
					// Each worker is below the budget; their sum must be refused.
					return nativeGroupRSS(ctx, cmd.Process.Pid, func(call, _, _ int, buf []byte) (int, error) {
						if call == procInfoListPIDs {
							binary.LittleEndian.PutUint32(buf, 10)
							binary.LittleEndian.PutUint32(buf[4:], 11)
							return 8, nil
						}
						binary.LittleEndian.PutUint64(buf[8:], 150<<20)
						return len(buf), nil
					})
				})
			})
			want, code := ErrMemoryLimit, 126
			if unavailable {
				want, code = ErrMemoryMonitor, 125
			}
			var refusal interface{ ExitCode() int }
			if !errors.Is(err, want) || !errors.As(err, &refusal) || refusal.ExitCode() != code {
				t.Fatalf("wrong refusal/code: %v (want %v, %d)", err, want, code)
			}
			if status, ok := leader.ProcessState.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
				t.Fatalf("leader not killed: %v", leader.ProcessState)
			}
			select {
			case err := <-joined:
				if err == nil || !strings.Contains(err.Error(), "killed") {
					t.Fatalf("helper escaped group kill: %v", err)
				}
			case <-time.After(time.Second):
				_ = helper.Process.Kill()
				<-joined
				t.Fatal("helper outlived memory refusal")
			}
		})
	}
}

func TestDarwinMemoryWatchJoinsOnCompletionAndCancellation(t *testing.T) {
	for _, finish := range []string{"completion", "cancellation", "timeout"} {
		t.Run(finish, func(t *testing.T) {
			cmd := exec.Command("/bin/sleep", "30")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			defer KillGroup(cmd)
			stop := make(chan struct{})
			timeout := make(chan time.Time, 1)
			joined := make(chan struct{})
			err := runCommand(stop, timeout, Options{}, cmd, func(_ Options, cmd *exec.Cmd) *memoryWatch {
				return watchGroupMemory(cmd, 256<<10, func(ctx context.Context, _ int) (uint64, error) {
					switch finish {
					case "completion":
						_ = cmd.Process.Signal(syscall.SIGTERM)
					case "cancellation":
						close(stop)
					case "timeout":
						timeout <- time.Now()
					}
					<-ctx.Done()
					close(joined)
					return 0, ctx.Err()
				})
			})
			select {
			case <-joined:
			default:
				t.Fatal("memory sample outlived command")
			}
			if finish != "completion" && !errors.Is(err, errStopped) {
				t.Fatalf("lost cancellation/timeout: %v", err)
			}
		})
	}
}
