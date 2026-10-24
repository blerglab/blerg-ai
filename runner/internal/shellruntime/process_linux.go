//go:build linux

package shellruntime

import (
	"context"
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
const TasksPerShell = 32
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
		ready <- err
		return
	}
	defer func() { _ = anchor.Close() }()
	rootFile, err := anchor.Open(".")
	if err != nil {
		ready <- err
		return
	}
	defer func() { _ = rootFile.Close() }()
	var rootStat unix.Stat_t
	if unix.Fstat(int(rootFile.Fd()), &rootStat) != nil || rootStat.Ino != rootIdentity.Inode || rootStat.Dev != rootIdentity.Device {
		ready <- ErrClosed
		return
	}
	rel, err := filepath.Rel(publicRoot, i.Cwd)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		ready <- ErrClosed
		return
	}
	dir, err := anchor.Open(rel)
	if err != nil {
		ready <- err
		return
	}
	defer func() { _ = dir.Close() }()
	cmd.Dir = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), dir.Fd())
	cmd.Env = i.Environment()
	attrs := &syscall.SysProcAttr{Ptrace: true, Setsid: true, Setctty: true, Ctty: 0, Pdeathsig: syscall.SIGKILL}
	if i.UID > 1<<32-1 {
		ready <- ErrClosed
		return
	}
	if uint64(os.Getuid()) != i.UID { //nolint:gosec // Linux getuid is a nonnegative uint32 represented as int by os.
		attrs.Credential = &syscall.Credential{Uid: uint32(i.UID), Gid: uint32(i.UID), Groups: []uint32{}}
		processcontainment.DropExecCapabilities(cmd)
	}

	start := func() error {
		tty, err := pty.StartWithAttrs(cmd, &pty.Winsize{Cols: 80, Rows: 24}, attrs)
		if err != nil {
			return err
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
			return ErrClosed
		}
		_ = tty.Close()
		p.tty = os.NewFile(uintptr(duplicate), "owner-shell-pty")
		return nil
	}
	_, err = p.tree.Run(cmd, start, TasksPerShell, ready)
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
	close(p.done)
}

func (p *tracedProcess) read() {
	defer close(p.output)
	buf := make([]byte, 4096)
	for {
		n, err := p.tty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			select {
			case p.output <- chunk:
			default:
				p.requestKill()
				return
			}
		}
		if err != nil {
			return
		}
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
