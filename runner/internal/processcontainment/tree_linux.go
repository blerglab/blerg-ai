//go:build linux

// Package processcontainment owns one kernel-traced descendant tree in a
// dedicated helper process. EXITKILL fences helper crashes; the inherited
// seccomp filter prevents CLONE_UNTRACED/clone3 escapes.
package processcontainment

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

var ErrUnproven = errors.New("process containment could not be proven")

type request struct {
	op  string
	ack chan error
}
type Tree struct {
	commands chan request
	done     chan struct{}
	err      error
}

func NewTree() *Tree { return &Tree{commands: make(chan request), done: make(chan struct{})} }
func (t *Tree) Control(ctx context.Context, op string) error {
	q := request{op: op, ack: make(chan error, 1)}
	select {
	case <-t.done:
		return t.err
	case t.commands <- q:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-q.ack:
		return err
	case <-t.done:
		return t.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run must execute on the helper's sole child-reaper thread. start creates the
// command with Ptrace=true (and optionally a PTY). ready means the kernel owns
// every descendant before its first instruction, not merely that Start returned.
func (t *Tree) Run(cmd *exec.Cmd, start func() error, maximum int, ready chan<- error) (code int, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() { t.err = err; close(t.done) }()
	fail := func(e error) (int, error) { ready <- e; return 1, e }
	if maximum < 1 || unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		return fail(ErrUnproven)
	}
	if e := InstallFilter(); e != nil {
		return fail(e)
	}
	if e := start(); e != nil {
		return fail(e)
	}
	root := cmd.Process.Pid
	defer func() { _ = cmd.Process.Release() }()
	var status unix.WaitStatus
	if _, e := unix.Wait4(root, &status, 0, nil); e != nil {
		_ = unix.Kill(root, unix.SIGKILL)
		return fail(ErrUnproven)
	}
	opts := unix.PTRACE_O_TRACEFORK | unix.PTRACE_O_TRACEVFORK | unix.PTRACE_O_TRACECLONE | unix.PTRACE_O_TRACEEXEC | unix.PTRACE_O_EXITKILL
	if unix.PtraceSetOptions(root, opts) != nil {
		_ = unix.Kill(root, unix.SIGKILL)
		_, _ = unix.Wait4(root, &status, 0, nil)
		return fail(ErrUnproven)
	}
	// Values retain the signal to deliver on thaw. -1 means still running or an
	// initial child stop not yet consumed. Never equate signal submission to freeze.
	tasks := map[int]int{root: -1}
	frozen, killing := false, false
	var pending *request
	ready <- nil
	if unix.PtraceCont(root, 0) != nil {
		killing = true
	}
	for {
		select {
		case q := <-t.commands:
			switch q.op {
			case "freeze":
				if pending != nil || killing {
					q.ack <- ErrUnproven
					break
				}
				frozen = true
				pending = &q
				for pid, sig := range tasks {
					if sig < 0 {
						_ = unix.Kill(pid, unix.SIGSTOP)
					}
				}
			case "thaw":
				if pending != nil || killing {
					q.ack <- ErrUnproven
					break
				}
				frozen = false
				for pid, sig := range tasks {
					if sig >= 0 {
						if unix.PtraceCont(pid, sig) != nil {
							killing = true
						}
						tasks[pid] = -1
					}
				}
				if killing {
					q.ack <- ErrUnproven
				} else {
					q.ack <- nil
				}
			case "stop":
				killing = true
				q.ack <- nil
			default:
				q.ack <- ErrUnproven
			}
		default:
		}
		if killing {
			for pid := range tasks {
				_ = unix.Kill(pid, unix.SIGKILL)
			}
		}
		if pending != nil {
			all := !killing
			for _, sig := range tasks {
				if sig < 0 {
					all = false
				}
			}
			if all {
				pending.ack <- nil
				pending = nil
			}
		}
		pid, e := unix.Wait4(-1, &status, unix.WNOHANG|unix.WALL, nil)
		if errors.Is(e, unix.EINTR) {
			continue
		}
		if errors.Is(e, unix.ECHILD) {
			if len(tasks) != 0 {
				return code, ErrUnproven
			}
			return code, nil
		}
		if e != nil {
			for pid := range tasks {
				_ = unix.Kill(pid, unix.SIGKILL)
			}
			return code, ErrUnproven
		}
		if pid == 0 {
			time.Sleep(time.Millisecond)
			continue
		}
		if status.Exited() || status.Signaled() {
			delete(tasks, pid)
			if pid == root {
				if status.Exited() {
					code = status.ExitStatus()
				} else {
					code = 128 + int(status.Signal())
				}
				killing = true
			}
			continue
		}
		if !status.Stopped() {
			continue
		}
		event := status.TrapCause()
		if event == unix.PTRACE_EVENT_EXEC {
			previous, e := unix.PtraceGetEventMsg(pid)
			if e != nil {
				killing = true
			} else {
				delete(tasks, int(previous))
			}
		}
		if event == unix.PTRACE_EVENT_FORK || event == unix.PTRACE_EVENT_VFORK || event == unix.PTRACE_EVENT_CLONE {
			child, e := unix.PtraceGetEventMsg(pid)
			if e != nil {
				killing = true
			} else if _, seen := tasks[int(child)]; !seen {
				tasks[int(child)] = -1
				if frozen {
					_ = unix.Kill(int(child), unix.SIGSTOP)
				}
			}
		}
		signal := int(status.StopSignal())
		if signal == int(unix.SIGTRAP) || signal == int(unix.SIGSTOP) {
			signal = 0
		}
		tasks[pid] = signal
		if len(tasks) > maximum {
			killing = true
		}
		if !frozen && !killing {
			if unix.PtraceCont(pid, signal) != nil {
				killing = true
			}
			tasks[pid] = -1
		}
	}
}
func (t *Tree) Stop(ctx context.Context) error {
	if err := t.Control(ctx, "stop"); err != nil {
		return err
	}
	select {
	case <-t.done:
		return t.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
