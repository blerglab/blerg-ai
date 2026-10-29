//go:build linux

package shellruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// A separate supervisor process owns this tracer thread. Every fork/clone is
// stopped by the kernel before it runs; EXITKILL also covers supervisor crashes.
// No /proc-name scan, PID reuse guess, or host docker-client kill is cleanup.
// TasksPerShell bounds one shell's processes and threads. Builds use about one thread per core, so it
// leaves room for real parallel work; a runaway fork still ends the shell, with the reason shown.
const TasksPerShell = 1024
const SessionTaskBudget = 4 * TasksPerShell

type tracedProcess struct {
	tree   *processcontainment.Tree
	tty    *os.File
	output chan []byte
	done   chan struct{}
	kill   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	err    error
	limit  string // set when the shell was ended for passing TasksPerShell
}

func startTraced(i Identity, rootIdentity protocol.WorkspaceRoot, publicRoot string) (*tracedProcess, error) {
	p := &tracedProcess{tree: processcontainment.NewTree(), output: make(chan []byte, 8), done: make(chan struct{}), kill: make(chan struct{})}
	ready := make(chan error, 1)
	go p.trace(i, rootIdentity, publicRoot, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	go p.read()
	return p, nil
}
func (p *tracedProcess) trace(i Identity, rootIdentity protocol.WorkspaceRoot, publicRoot string, ready chan<- error) {
	cmd := exec.Command(i.Shell, "-i") //nolint:gosec,noctx // resolved local account shell, fixed interactive non-login argv; tracer owns lifetime
	anchor, err := os.OpenRoot(publicRoot)
	if err != nil {
		ready <- stageError("open_root", err)
		return
	}
	defer func() { _ = anchor.Close() }()
	rootFile, err := anchor.Open(".")
	if err != nil {
		ready <- stageError("open_root", err)
		return
	}
	defer func() { _ = rootFile.Close() }()
	var rootStat unix.Stat_t
	if unix.Fstat(int(rootFile.Fd()), &rootStat) != nil || rootStat.Ino != rootIdentity.Inode || rootStat.Dev != rootIdentity.Device {
		ready <- stageError("verify_root", nil)
		return
	}
	rel, err := filepath.Rel(publicRoot, i.Cwd)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		ready <- stageError("verify_cwd", err)
		return
	}
	dir, err := anchor.Open(rel)
	if err != nil {
		ready <- stageError("open_cwd", err)
		return
	}
	defer func() { _ = dir.Close() }()
	cmd.Dir = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), dir.Fd())
	cmd.Env = i.Environment()
	attrs := &syscall.SysProcAttr{Ptrace: true, Setsid: true, Setctty: true, Ctty: 0, Pdeathsig: syscall.SIGKILL}
	if i.UID > 1<<32-1 {
		ready <- stageError("verify_uid", nil)
		return
	}
	if uint64(os.Getuid()) != i.UID { //nolint:gosec // Linux getuid is a nonnegative uint32 represented as int by os.
		attrs.Credential = &syscall.Credential{Uid: uint32(i.UID), Gid: uint32(i.UID), Groups: []uint32{}}
		processcontainment.DropExecCapabilities(cmd)
	}

	start := func() error { return p.startPTY(cmd, attrs, pty.Open) }
	containmentReady := make(chan error, 1)
	go func() {
		err := <-containmentReady
		if err != nil {
			var staged *startError
			if !errors.As(err, &staged) {
				err = stageError("containment_setup", err)
			}
		}
		ready <- err
	}()
	_, err = p.tree.Run(cmd, start, TasksPerShell, containmentReady)
	p.mu.Lock()
	if errors.Is(err, processcontainment.ErrTaskLimit) {
		// The tree is proven empty; this is a reason to show, not a containment failure.
		p.limit, err = processcontainment.TaskLimitMessage(TasksPerShell, "shell"), nil
	}
	p.err = err
	p.mu.Unlock()
	close(p.done)
}

// Keep allocation separate from exec so a service's device denial is distinct
// from an account shell or tracing denial. open is fixed to pty.Open in production.
func (p *tracedProcess) startPTY(cmd *exec.Cmd, attrs *syscall.SysProcAttr, open func() (*os.File, *os.File, error)) error {
	tty, slave, err := open()
	if err != nil {
		return stageError("pty_open", err)
	}
	defer func() { _ = slave.Close() }()
	if err = pty.Setsize(tty, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		_ = tty.Close()
		return stageError("pty_configure", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = attrs
	if err = cmd.Start(); err != nil {
		_ = tty.Close()
		return stageError("exec_shell", err)
	}
	duplicate, e := unix.Dup(int(tty.Fd()))
	if e == nil {
		unix.CloseOnExec(duplicate)
		e = unix.SetNonblock(duplicate, true)
	}
	if e != nil {
		if duplicate >= 0 {
			_ = unix.Close(duplicate)
		}
		_ = tty.Close()
		_ = cmd.Process.Kill()
		return stageError("pty_configure", e)
	}
	_ = tty.Close()
	p.tty = os.NewFile(uintptr(duplicate), "owner-shell-pty")
	return nil
}

func (p *tracedProcess) read() {
	defer close(p.output)
	buf := make([]byte, 4096)
	for {
		n, err := p.tty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			// A full queue is backpressure, not a failure: while this reader waits,
			// the PTY buffer fills and the program blocks in write, as on a real
			// terminal. Containment never waits on it: requestKill closes kill.
			select {
			case p.output <- chunk:
			case <-p.kill:
				return
			}
		}
		if err != nil {
			p.sayLimit()
			return
		}
	}
}

// sayLimit writes the task-limit reason into the terminal once the tree has ended for it, so the
// person sees why the shell stopped. The PTY closes as the tree dies, so wait (best effort) for the verdict.
func (p *tracedProcess) sayLimit() {
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		return
	}
	p.mu.Lock()
	msg := p.limit
	p.mu.Unlock()
	if msg == "" {
		return
	}
	select {
	case p.output <- []byte("\r\n" + msg + "\r\n"):
	case <-p.kill:
	}
}
func (p *tracedProcess) requestKill() {
	p.once.Do(func() {
		close(p.kill)
		_ = p.tty.Close()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.tree.Stop(ctx)
		}()
	})
}
func (p *tracedProcess) stop(ctx context.Context) error {
	p.requestKill() //nolint:contextcheck // Independent containment cleanup must continue after the caller cancels.
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *tracedProcess) input(data []byte) error {
	if len(data) == 0 || len(data) > 16384 {
		return ErrClosed
	}
	if err := p.tty.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	_, err := p.tty.Write(data)
	return err
}
func (p *tracedProcess) resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return ErrClosed
	}
	raw, err := p.tty.SyscallConn()
	if err != nil {
		return err
	}
	var resizeErr error
	if err = raw.Control(func(fd uintptr) {
		resizeErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows})
	}); err != nil {
		return err
	}
	return resizeErr
}

func matchesRoot(path string, root protocol.WorkspaceRoot) bool {
	var st unix.Stat_t
	return root.Inode > 0 && unix.Stat(path, &st) == nil && st.Dev == root.Device && st.Ino == root.Inode
}

func (p *tracedProcess) freeze(ctx context.Context) error { return p.tree.Control(ctx, "freeze") }
func (p *tracedProcess) thaw(ctx context.Context) error   { return p.tree.Control(ctx, "thaw") }
