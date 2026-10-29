//go:build linux

package processcontainment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// WriterTaskLimit bounds the tasks (threads count) one traced command may hold. Real parallel builds
// (a Go toolchain uses about one thread per core) stay well under it; a runaway fork is still stopped.
const WriterTaskLimit = 4096

// TaskLimitMessage is the plain line shown when a traced tree is ended for passing its task limit.
// scope names what was stopped ("session", "shell").
func TaskLimitMessage(limit int, scope string) string {
	return fmt.Sprintf("blerg: process limit reached: more than %d processes and threads in this %s, so it was stopped. Retry with less parallelism (for example GOMAXPROCS=4, go -p 1, make -j4, fewer test workers).", limit, scope)
}

// Group is generation-local. Wrapping creation and freezing serialize on this
// mutex; a pending launch must acknowledge readiness or cleanup stays unproven.
type Group struct {
	BeforeExec     func(int) error // durable owned-helper identity before any untrusted exec
	mu             sync.Mutex
	peers          []*peer
	executable     string
	uid, gid       uint32
	frozen, closed bool
}
type frame struct {
	Op     string
	OK     bool
	Detail string `json:",omitempty"`
}
type peer struct {
	mu            sync.Mutex
	parent, child *os.File
	replies       chan frame
	ended         chan struct{}
	empty         atomic.Bool
	failure       string // published by closing ended
}

func NewGroup(executable string, uid, gid uint32) *Group {
	return &Group{executable: executable, uid: uid, gid: gid}
}

// Start owns both descriptor inheritance and its post-Start close. Closing an
// ExtraFile on a child-ready message races the parent's exec descriptor setup.
func (g *Group) Start(cmd *exec.Cmd) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, err := g.wrap(cmd)
	if err != nil {
		return err
	}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return p.call(ctx, "stop")
		}
	}
	bootSent := false
	err = cmd.Start()
	if err == nil && g.BeforeExec != nil {
		if err = g.BeforeExec(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		bootSent = true
		err = p.call(ctx, "start")
		cancel()
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	_ = p.child.Close()
	if err != nil {
		// A lost startup reply may follow an executed writer. Only a failure
		// before bootstrap proves no untrusted process could have started.
		if !bootSent {
			p.empty.Store(true)
		}
		_ = p.parent.Close()
	}
	return err
}
func (g *Group) wrap(cmd *exec.Cmd) (*peer, error) {
	if g.frozen || g.closed || cmd == nil || cmd.Path == "" || len(cmd.ExtraFiles) != 0 {
		return nil, fmt.Errorf("%w: writer admission closed or command descriptors unsupported", ErrUnproven)
	}
	live := g.peers[:0]
	for _, p := range g.peers {
		if p.empty.Load() {
			_ = p.parent.Close()
		} else {
			live = append(live, p)
		}
	}
	g.peers = live
	if len(g.peers) >= 128 {
		return nil, fmt.Errorf("%w: writer helper limit reached", ErrUnproven)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	_ = unix.SetNonblock(pair[0], true)
	_ = unix.SetNonblock(pair[1], true)
	p := &peer{replies: make(chan frame, 8), ended: make(chan struct{}), parent: os.NewFile(uintptr(pair[0]), "writer-control"), child: os.NewFile(uintptr(pair[1]), "writer-child")}
	go func() {
		defer close(p.ended)
		decoder := json.NewDecoder(p.parent)
		for {
			var f frame
			if decoder.Decode(&f) != nil {
				return
			}
			if f.Op == "ready" {
				continue
			}
			if f.Op == "empty" && f.OK {
				p.empty.Store(true)
				return
			}
			if !f.OK {
				p.failure = f.Detail
			}
			select {
			case p.replies <- f:
			default:
				return
			}
		}
	}()
	g.peers = append(g.peers, p)
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, p.child)
	cmd.Args = append([]string{g.executable, "cluster-writer", strconv.Itoa(fd), strconv.FormatUint(uint64(g.uid), 10), strconv.FormatUint(uint64(g.gid), 10), cmd.Path}, cmd.Args[1:]...)
	cmd.Path = g.executable
	cmd.Env = HelperEnvironment(cmd.Env)
	// Parent cancellation kills this helper; EXITKILL then fences its whole
	// descendant tree. Settlement still requires its explicit empty receipt.
	return p, nil
}
func (p *peer) call(ctx context.Context, op string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.empty.Load() {
		return nil
	}
	if err := p.parent.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	if err := json.NewEncoder(p.parent).Encode(frame{Op: op}); err != nil {
		select {
		case <-p.ended:
			if p.empty.Load() {
				return nil
			}
		case <-ctx.Done():
		}
		return fmt.Errorf("%w: %s control write failed: %w", ErrUnproven, op, err)
	}
	select {
	case <-p.ended:
		if p.empty.Load() {
			return nil
		}
		return fmt.Errorf("%w: %s control channel closed without an empty receipt: %s", ErrUnproven, op, p.failure)
	case f := <-p.replies:
		if f.Op != op || !f.OK {
			return fmt.Errorf("%w: %s received %s (ok=%t): %s", ErrUnproven, op, f.Op, f.OK, f.Detail)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: waiting for %s helper reply: %w", ErrUnproven, op, ctx.Err())
	}
}
func (g *Group) control(ctx context.Context, op string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if op == "freeze" {
		if g.closed {
			return fmt.Errorf("%w: freeze requested after writer group closed", ErrUnproven)
		}
		g.frozen = true
	}
	if op == "stop" {
		g.closed = true
	}
	var result error
	for i, p := range g.peers {
		if err := p.call(ctx, op); err != nil {
			result = errors.Join(result, fmt.Errorf("writer helper %d: %w", i+1, err))
		}
	}
	if result == nil && op == "thaw" {
		g.frozen = false
	}
	if result == nil && op == "stop" {
		for _, p := range g.peers {
			_ = p.parent.Close()
		}
	}
	return result
}
func (g *Group) Freeze(ctx context.Context) error { return g.control(ctx, "freeze") }
func (g *Group) Thaw(ctx context.Context) error   { return g.control(ctx, "thaw") }
func (g *Group) Stop(ctx context.Context) error   { return g.control(ctx, "stop") }

// ServeCommand runs only as an inherited-pipe helper, outside the agent UID.
// The pipe is never inherited by the engine. Input/outputs retain their original
// descriptors; no agent bytes travel on this control channel.
func ServeCommand(args []string) int {
	if len(args) < 4 {
		return 125
	}
	fd, e := strconv.Atoi(args[0])
	if e != nil || fd < 3 {
		return 125
	}
	uid, e := strconv.ParseUint(args[1], 10, 32)
	if e != nil {
		return 125
	}
	gid, e := strconv.ParseUint(args[2], 10, 32)
	if e != nil {
		return 125
	}
	control := os.NewFile(uintptr(fd), "writer-control")
	if control == nil {
		return 125
	}
	unix.CloseOnExec(fd)
	defer func() { _ = control.Close() }()
	var writeMu sync.Mutex
	emit := func(f frame) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = control.SetWriteDeadline(time.Now().Add(time.Second))
		_ = json.NewEncoder(control).Encode(f)
	}
	decoder := json.NewDecoder(io.LimitReader(control, 1<<20))
	var boot frame
	if decoder.Decode(&boot) != nil || boot.Op != "start" {
		return 125
	}
	cmd := exec.Command(args[3], args[4:]...) //nolint:gosec,noctx // trusted parent command, inherited pipe; traced tree owns lifetime.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	attrs := &syscall.SysProcAttr{Ptrace: true, Pdeathsig: syscall.SIGKILL, Setsid: true}
	if uint64(os.Getuid()) != uid { //nolint:gosec // Linux getuid is an unsigned 32-bit UID represented as int by os.
		attrs.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}
		DropExecCapabilities(cmd)
	}
	cmd.SysProcAttr = attrs
	tree := NewTree()
	ready := make(chan error, 1)
	done := make(chan struct{})
	code, runErr := 125, error(nil)
	go func() { code, runErr = tree.Run(cmd, cmd.Start, WriterTaskLimit, ready); close(done) }()
	if err := <-ready; err != nil {
		<-done
		emit(resultFrame("empty", runErr))
		return 125
	}
	emit(frame{Op: "start", OK: true})
	requests := make(chan frame)
	go func() {
		defer close(requests)
		for {
			var f frame
			if decoder.Decode(&f) != nil {
				return
			}
			select {
			case requests <- f:
			case <-done:
				return
			}
		}
	}()
	for {
		select {
		case <-done:
			if errors.Is(runErr, ErrTaskLimit) {
				// The tree is gone (proven empty); say why on the command's own stderr, which the
				// daemon shows with the exit, so the person and the agent can lower parallelism.
				// A leading newline keeps it off any half-written engine line.
				_, _ = fmt.Fprint(os.Stderr, "\n"+TaskLimitMessage(WriterTaskLimit, "session")+"\n")
				emit(frame{Op: "empty", OK: true, Detail: runErr.Error()})
				return code
			}
			emit(resultFrame("empty", runErr))
			return code
		case f, ok := <-requests:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if !ok || f.Op == "stop" {
				err := tree.Stop(ctx)
				cancel()
				emit(resultFrame("empty", err))
				return 0
			}
			err := tree.Control(ctx, f.Op)
			cancel()
			emit(resultFrame(f.Op, err))
		}
	}
}

// Retain the trusted helper's failed check across the control channel.
func resultFrame(op string, err error) frame {
	f := frame{Op: op, OK: err == nil}
	if err != nil {
		f.Detail = err.Error()
	}
	return f
}
