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

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// A separate supervisor process owns this tracer thread. Every fork/clone is
// stopped by the kernel before it runs; EXITKILL also covers supervisor crashes.
// No /proc-name scan, PID reuse guess, or host docker-client kill is cleanup.
// TasksPerShell bounds one shell's processes and threads. Builds use about one thread per core, so it
// leaves room for real parallel work; past it a new process is refused, with the reason shown.
const TasksPerShell = 1024
const SessionTaskBudget = 4 * TasksPerShell

// shellTree is what a shell needs from its containment: the tracer, or a cgroup for a host shell
// that may use sudo.
type shellTree interface {
	Run(cmd *exec.Cmd, start func() error, maximum int, ready chan<- error) (int, error)
	AtLimit() <-chan struct{}
	Control(ctx context.Context, op string) error
	Stop(ctx context.Context) error
}

type tracedProcess struct {
	tree   shellTree
	cgroup *cgroupTree
	tty    *os.File
	output chan []byte
	done   chan struct{}
	kill   chan struct{}
	// readEnded and sayEnded order the two writers of output: read closes it only after sayLimit is gone.
	readEnded, sayEnded chan struct{}

	once sync.Once
	mu   sync.Mutex
	err  error
}

func startTraced(i Identity, rootIdentity protocol.WorkspaceRoot, publicRoot string) (*tracedProcess, error) {
	return startShell(i, rootIdentity, publicRoot, "")
}

// startShell holds the shell in cgroup, or under the tracer when cgroup is empty.
func startShell(i Identity, rootIdentity protocol.WorkspaceRoot, publicRoot, cgroup string) (*tracedProcess, error) {
	p := &tracedProcess{tree: processcontainment.NewTree(), output: make(chan []byte, 8), done: make(chan struct{}), kill: make(chan struct{}), readEnded: make(chan struct{}), sayEnded: make(chan struct{})}
	if cgroup != "" {
		p.cgroup = newCgroupTree(cgroup)
		p.tree = p.cgroup
	}
	ready := make(chan error, 1)
	go p.trace(i, rootIdentity, publicRoot, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	go p.read()
	return p, nil
}
func (p *tracedProcess) trace(i Identity, rootIdentity protocol.WorkspaceRoot, publicRoot string, ready chan<- error) {
	if tree, ok := p.tree.(*processcontainment.Tree); ok {
		if file, record := shellProcessRecorder(); file != nil {
			defer func() { _ = file.Close() }()
			tree.Ledger = record
		}
	}
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
	if i.MemberRoot.Inode != 0 {
		var member unix.Stat_t
		if unix.Fstat(int(dir.Fd()), &member) != nil || member.Dev != i.MemberRoot.Device || member.Ino != i.MemberRoot.Inode {
			ready <- stageError("verify_cwd", nil)
			return
		}
	}
	// The forked child resolves its own inherited descriptor before exec closes
	// CLOEXEC descriptors. Resolving the nondumpable parent's /proc entry would
	// deny the chdir; pathname fallback would lose the pinned-directory guarantee.
	cmd.Dir = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), dir.Fd())
	if i.MemberRoot.Inode != 0 || uint64(os.Getuid()) == i.UID { //nolint:gosec // Linux getuid is a nonnegative uint32.
		cmd.Dir = fmt.Sprintf("/proc/self/fd/%d", dir.Fd())
	}
	cmd.Env = i.Environment()
	attrs := &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0, Pdeathsig: syscall.SIGKILL}
	if i.UID > 1<<32-1 {
		ready <- stageError("verify_uid", nil)
		return
	}
	if uint64(os.Getuid()) != i.UID { //nolint:gosec // Linux getuid is a nonnegative uint32 represented as int by os.
		attrs.Credential = &syscall.Credential{Uid: uint32(i.UID), Gid: uint32(i.UID), Groups: []uint32{}}
		processcontainment.DropExecCapabilities(cmd)
	}
	if p.cgroup != nil {
		if err = p.cgroup.place(attrs); err != nil {
			ready <- stageError("containment_setup", err)
			return
		}
	}

	start := func() error { return p.startPTY(cmd, attrs, pty.Open) }
	containmentReady := make(chan error, 1)
	go func() {
		err := <-containmentReady
		if err != nil {
			var staged *startError
			switch {
			case errors.As(err, &staged):
			case errors.Is(err, processcontainment.ErrStart):
				err = stageError("exec_shell", err)
			default:
				err = stageError("containment_setup", err)
			}
		}
		ready <- err
	}()
	go p.sayLimit()
	_, err = p.tree.Run(cmd, start, TasksPerShell, containmentReady)
	p.mu.Lock()
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
	defer func() {
		close(p.readEnded)
		<-p.sayEnded
	}()
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
			return
		}
	}
}

// sayLimit writes the reason into the terminal when the tree refuses a process, so the person sees
// why a command failed with "Resource temporarily unavailable". The shell goes on.
func (p *tracedProcess) sayLimit() {
	defer close(p.sayEnded)
	for {
		select {
		case <-p.tree.AtLimit():
		case <-p.readEnded:
			return
		case <-p.done:
			return
		}
		select {
		case p.output <- []byte("\r\n" + processcontainment.TaskLimitMessage(TasksPerShell) + "\r\n"):
		case <-p.readEnded:
			return
		case <-p.done:
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

// tryInput performs one nonblocking syscall so a persistent lease can serialize
// the actual write with epoch changes without blocking its lifetime timer.
func (p *tracedProcess) tryInput(data []byte) (int, error) {
	raw, err := p.tty.SyscallConn()
	if err != nil {
		return 0, err
	}
	var count int
	var writeErr error
	err = raw.Control(func(fd uintptr) { count, writeErr = unix.Write(int(fd), data) })
	if err != nil {
		return 0, err
	}
	return count, writeErr
}

func persistentRootError(path string, root protocol.WorkspaceRoot) error {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return ErrIdentityChanged
		}
		return ownershell.ErrUnknown
	}
	if root.Inode == 0 || st.Ino != root.Inode || st.Dev != root.Device {
		return ErrIdentityChanged
	}
	return nil
}

func persistentMemberRoot(path string) (protocol.WorkspaceRoot, error) {
	var st unix.Stat_t
	if unix.Stat(path, &st) != nil || st.Ino == 0 {
		return protocol.WorkspaceRoot{}, ownershell.ErrUnknown
	}
	return protocol.WorkspaceRoot{Device: st.Dev, Inode: st.Ino}, nil
}
