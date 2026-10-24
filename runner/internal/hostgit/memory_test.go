//go:build darwin || linux

package hostgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDarwinRSSCommand(t *testing.T) {
	cmd := darwinRSSCommand(t.Context(), 12345)
	if cmd.Path != "/bin/ps" || !reflect.DeepEqual(cmd.Args, []string{"/bin/ps", "-o", "rss=", "-g", "12345"}) {
		t.Fatalf("not a fixed Darwin process-group sample: %v", cmd)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}) || cmd.WaitDelay != memorySampleTimeout {
		t.Fatalf("unbounded/inherited sample environment: %v", cmd)
	}
}

func TestDarwinRSSParser(t *testing.T) {
	for _, raw := range []string{"RSS\n10\n", "-1\n", "1 2\n", "1.5\n", "18446744073709551615\n1\n", "18446744073709551616\n"} {
		if _, err := parseGroupRSS(raw); err == nil {
			t.Errorf("accepted invalid RSS: %q", raw)
		}
	}
	if rss, err := parseGroupRSS("  153600\n\t153600\n 0\n"); err != nil || rss != 300<<10 {
		t.Fatalf("did not sum helpers in KiB: %d %v", rss, err)
	}
}

func TestDarwinRSSObservationBounds(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		want         uint64
		fail         bool
	}{
		{"group", "printf ' 100\\n 200\\n'", 300, false},
		{"departed", "exit 1", 0, false},
		{"permission", "printf denied >&2; exit 1", 0, true},
		{"failure", "exit 2", 0, true},
		{"success-with-error", "printf failure >&2; exit 0", 0, true},
		{"empty-success", "exit 0", 0, true},
		{"malformed", "printf 'rss\\n'", 0, true},
		{"overflow", "i=0; while [ $i -lt 20000 ]; do printf '1234\\n'; i=$((i+1)); done", 0, true},
		{"blocked", "exec /bin/sleep 30", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			rss, err := readGroupRSS(t.Context(), 12345, func(ctx context.Context, pgid int) *exec.Cmd {
				if pgid != 12345 {
					t.Errorf("sampled wrong process group: %d", pgid)
				}
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", tc.script)
				cmd.WaitDelay = memorySampleTimeout
				return cmd
			})
			if (err != nil) != tc.fail || rss != tc.want || time.Since(start) > time.Second {
				t.Fatalf("RSS=%d err=%v elapsed=%s", rss, err, time.Since(start))
			}
		})
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
				return watchGroupMemory(cmd, memoryLimitKB(o), func(_ context.Context, pgid int) (uint64, error) {
					if pgid != cmd.Process.Pid {
						t.Errorf("watching wrong group: %d", pgid)
					}
					if unavailable {
						return 0, errors.New("injected sample failure")
					}
					// Each worker is below the budget; their sum must be refused.
					return parseGroupRSS("153600\n153600\n")
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
