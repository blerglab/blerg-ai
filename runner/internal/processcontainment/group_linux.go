//go:build linux

package processcontainment

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// controlTimeout bounds one freeze, thaw or stop. A freeze waits for every traced task to reach a
// stop; on a busy machine with hundreds of threads that can take more than a few seconds.
const controlTimeout = 30 * time.Second

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
	Label          string          // names the owner (a session) in log lines
	mu             sync.Mutex
	peers          []*peer
	executable     string
	uid, gid       uint32
	frozen, closed bool
}

// Census is diagnostic task accounting, never a containment proof.
// Tracked and Live count the tracer's and the kernel's tasks at one instant of a census. Untracked
// counts live tasks the tracer does not account for (while frozen, a task that could be running);
// Exited counts tracked tasks committed to exit that left /proc during the scan; Lost counts any
// other tracked task the kernel no longer has (an accounting leak).
type Census struct {
	Tracked, Live           int64
	Untracked, Exited, Lost int64
	Mismatch                string `json:",omitempty"` // the tasks the two disagree on, with their kernel state
}

type frame struct {
	Census Census

	Op     string
	OK     bool
	Detail string `json:",omitempty"`
}
type peer struct {
	mu            sync.Mutex
	parent, child *os.File
	replies       chan frame
	ended         chan struct{}
	settled       chan struct{} // closed once a lost helper's tree is proven gone (empty is then true)
	empty         atomic.Bool
	pid           atomic.Int64
	census        Census // last serialized diagnostic reply
	failure       string // published by closing ended
	label         string
	helper        atomic.Int64 // pidfd of the helper process; -1 when unavailable
	ledger        pidLedger
}

// pidLedger holds a pidfd for every process the helper's tree ever ran, each received before that
// process ran its first instruction. If the helper is lost, its tree is proven gone once the helper
// has exited, the control stream was read to its end intact, and every recorded pidfd reports
// that its process has exited:
//   - the tracer continues no task whose process it could not record, so a process absent from
//     the ledger never ran user code. When the tracer exits, PTRACE_O_EXITKILL kills every tracee
//     under the tasklist lock; a fork racing it is either linked as a tracee first (and killed
//     before it can run) or fails because its parent already has SIGKILL pending.
//   - a pidfd reports POLLIN only once its whole thread group has exited, after its memory and
//     files are released, so a recorded process can no longer write.
type pidLedger struct {
	mu      sync.Mutex
	fds     []int
	records int // ever received; zero means the helper ran without a ledger
	pruneAt int
	broken  string // why the ledger cannot be trusted; never cleared
}

func (l *pidLedger) receive(oob []byte, flags int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if flags&unix.MSG_CTRUNC != 0 {
		l.breakLocked("control message truncated")
	}
	messages, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		l.breakLocked("control message unreadable: " + err.Error())
		return
	}
	for i := range messages {
		fds, err := unix.ParseUnixRights(&messages[i])
		if err != nil {
			l.breakLocked("descriptor message unreadable: " + err.Error())
			continue
		}
		l.fds = append(l.fds, fds...)
		l.records += len(fds)
	}
	if len(l.fds) >= l.pruneAt {
		l.fds, _ = exited(l.fds, 0, true)
		l.pruneAt = max(64, 2*len(l.fds))
	}
}
func (l *pidLedger) breakLocked(reason string) {
	if l.broken == "" {
		l.broken = reason
	}
}
func (l *pidLedger) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, fd := range l.fds {
		_ = unix.Close(fd)
	}
	l.fds = nil
}

// exited polls pidfds for at most timeout milliseconds and returns those whose process has not
// exited. With release set, it closes the others. On a poll failure every unpolled pidfd is alive.
func exited(fds []int, timeout int, release bool) ([]int, error) {
	alive := fds[:0]
	for start := 0; start < len(fds); start += 1024 {
		batch := fds[start:min(start+1024, len(fds))]
		polls := make([]unix.PollFd, len(batch))
		for i, fd := range batch {
			polls[i] = unix.PollFd{Fd: int32(fd), Events: unix.POLLIN} //nolint:gosec // Linux file descriptors are signed int32.
		}
		if _, err := unix.Poll(polls, timeout); err != nil && !errors.Is(err, unix.EINTR) {
			return append(alive, fds[start:]...), err
		}
		for i, fd := range batch {
			switch {
			case polls[i].Revents&unix.POLLIN == 0:
				alive = append(alive, fd)
			case release:
				_ = unix.Close(fd)
			}
		}
	}
	return alive, nil
}

// streamReader reads the control stream with recvmsg so the ledger's descriptors are kept.
type streamReader struct {
	raw    syscall.RawConn
	ledger *pidLedger
	oob    []byte
}

func (r *streamReader) Read(b []byte) (int, error) {
	var n, oobn, flags int
	var readErr error
	if err := r.raw.Read(func(fd uintptr) bool {
		n, oobn, flags, _, readErr = unix.Recvmsg(int(fd), b, r.oob, unix.MSG_CMSG_CLOEXEC)
		return !errors.Is(readErr, unix.EAGAIN) && !errors.Is(readErr, unix.EINTR)
	}); err != nil {
		return 0, err
	}
	if readErr != nil {
		return 0, readErr
	}
	if oobn > 0 || flags&unix.MSG_CTRUNC != 0 {
		r.ledger.receive(r.oob[:oobn], flags)
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
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
	if err == nil {
		p.pid.Store(int64(cmd.Process.Pid))
		// Nobody can reap the helper before Start returns, so this is the helper, never a reused PID.
		// Without it a lost helper's tree is never proven gone (fail closed), as before the ledger.
		if helper, e := unix.PidfdOpen(cmd.Process.Pid, 0); e == nil {
			p.helper.Store(int64(helper))
		}
	}
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
			p.ledger.close()
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
	p := &peer{replies: make(chan frame, 8), ended: make(chan struct{}), settled: make(chan struct{}), parent: os.NewFile(uintptr(pair[0]), "writer-control"), child: os.NewFile(uintptr(pair[1]), "writer-child"), label: g.Label}
	p.helper.Store(-1)
	p.ledger.pruneAt = 64
	raw, err := p.parent.SyscallConn()
	if err != nil {
		_ = p.parent.Close()
		_ = p.child.Close()
		return nil, err
	}
	go p.read(&streamReader{raw: raw, ledger: &p.ledger, oob: make([]byte, unix.CmsgSpace(16*4))})
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
func (p *peer) logf(format string, args ...any) {
	prefix := fmt.Sprintf("writer helper pid=%d", p.pid.Load())
	if p.label != "" {
		prefix += " (" + p.label + ")"
	}
	log.Printf(prefix+": "+format, args...)
}

// read follows the helper's replies until its empty receipt. Without one, it reads the stream to its
// end so the ledger holds every recorded process, then proves the lost helper's tree gone.
func (p *peer) read(r *streamReader) {
	decoder := json.NewDecoder(r)
	for {
		var f frame
		if decoder.Decode(&f) != nil {
			break
		}
		if f.Op == "ready" {
			continue
		}
		if f.Op == "empty" && f.OK {
			p.empty.Store(true)
			if f.Detail != "" {
				p.logf("tree ended with an empty receipt: %s", f.Detail)
			} else {
				p.logf("tree ended with an empty receipt")
			}
			p.ledger.close()
			p.release()
			close(p.ended)
			close(p.settled)
			return
		}
		if !f.OK {
			p.failure = f.Detail
		}
		select {
		case p.replies <- f:
		default:
			p.failure = "unexpected reply " + f.Op
		}
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		p.ledger.mu.Lock()
		p.ledger.breakLocked("control stream read failed: " + err.Error())
		p.ledger.mu.Unlock()
	}
	if !p.empty.Load() {
		p.logf("control stream closed without an empty receipt: %s", cmp.Or(p.failure, "the helper exited"))
	}
	close(p.ended)
	p.settle()
}

// settle proves a lost helper's tree gone (see pidLedger), then marks the peer empty. It waits as
// long as that takes: a killed task in uninterruptible sleep exits only when its I/O returns.
func (p *peer) settle() {
	if p.empty.Load() { // never started a writer
		p.release()
		close(p.settled)
		return
	}
	p.ledger.mu.Lock()
	broken := p.ledger.broken
	if broken == "" && p.ledger.records == 0 {
		broken = "the helper kept no process ledger"
	}
	p.ledger.mu.Unlock()
	helper := int(p.helper.Load())
	if helper < 0 || broken != "" {
		p.logf("its processes cannot be proven gone: %s", cmp.Or(broken, "helper pidfd unavailable"))
		return
	}
	start, warned := time.Now(), false
	for alive := []int{helper}; len(alive) != 0; {
		var err error
		if alive, err = exited(alive, 100, false); err != nil {
			p.logf("its processes cannot be proven gone: helper poll: %v", err)
			return
		}
		if !warned && time.Since(start) > 5*time.Second {
			p.logf("still waiting for the lost helper to exit")
			warned = true
		}
	}
	p.ledger.mu.Lock()
	fds := p.ledger.fds
	p.ledger.fds = nil
	p.ledger.mu.Unlock()
	recorded := len(fds)
	for len(fds) != 0 {
		var err error
		if fds, err = exited(fds, 100, true); err != nil {
			p.ledger.mu.Lock()
			p.ledger.fds = fds // closed with the peer
			p.ledger.mu.Unlock()
			p.logf("its processes cannot be proven gone: ledger poll: %v", err)
			return
		}
		if !warned && time.Since(start) > 5*time.Second && len(fds) != 0 {
			p.logf("still waiting for %d of its processes to exit", len(fds))
			warned = true
		}
	}
	p.empty.Store(true)
	p.release()
	p.logf("lost without an empty receipt; its processes are proven gone (%d still recorded at loss)", recorded)
	close(p.settled)
}
func (p *peer) release() {
	if fd := p.helper.Swap(-1); fd >= 0 {
		_ = unix.Close(int(fd))
	}
}

func (p *peer) call(ctx context.Context, op string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.empty.Load() {
		return nil
	}
	if op == "stop" {
		p.logf("stop requested")
	}
	select {
	case <-p.ended:
		return p.lost(ctx, op)
	default:
	}
	if err := p.parent.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	if err := json.NewEncoder(p.parent).Encode(frame{Op: op}); err != nil {
		select {
		case <-p.ended:
			return p.lost(ctx, op)
		case <-ctx.Done():
		}
		return fmt.Errorf("%w: %s control write failed: %w", ErrUnproven, op, err)
	}
	select {
	case <-p.ended:
		return p.lost(ctx, op)
	case f := <-p.replies:
		p.census = f.Census
		if f.Op != op || !f.OK {
			return fmt.Errorf("%w: %s received %s (ok=%t): %s", ErrUnproven, op, f.Op, f.OK, f.Detail)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: waiting for %s helper reply: %w", ErrUnproven, op, ctx.Err())
	}
}

// lost answers for a helper whose stream ended. Its end alone does not prove the traced descendants
// absent: freeze and stop wait for the ledger's proof, so a turn's sync still needs it. Thaw
// succeeds without it, by design. Thaw promises nothing about absence: it only lets writers run
// again, and a dead helper has none to continue (the tracer is gone and EXITKILL ended its
// tracees). Holding admission closed on it would gain no safety (the next freeze still waits
// for the proof) and would refuse every later engine, which is how a lost helper used to end
// the session. failure is published by ended; callers must observe that channel before reading it.
func (p *peer) lost(ctx context.Context, op string) error {
	if p.empty.Load() || op == "thaw" {
		return nil
	}
	select {
	case <-p.settled:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %s: helper exited without an empty receipt and its processes are not yet proven gone: %s", ErrUnproven, op, p.failure)
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
			result = errors.Join(result, fmt.Errorf("writer helper %d (pid %d): %w", i+1, p.pid.Load(), err))
		}
	}
	if result == nil && op == "thaw" {
		g.frozen = false
	}
	if result == nil && op == "stop" {
		for _, p := range g.peers {
			p.ledger.close()
			_ = p.parent.Close()
		}
	}
	return result
}

// Census reads each helper's serialized accounting. Call while frozen for a stable sample.
func (g *Group) Census(ctx context.Context) ([]Census, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var result []Census
	for _, p := range g.peers {
		if err := p.call(ctx, "census"); err != nil {
			return nil, err
		}
		if !p.empty.Load() {
			result = append(result, p.census)
		}
	}
	return result, nil
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
	// The control descriptor is blocking here (the parent handed it over with Fd), so a full socket
	// stalls the tracer, and with it the tree, rather than dropping a record.
	tree.Ledger = func(pidfd int) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		for {
			err := unix.Sendmsg(fd, []byte{'\n'}, unix.UnixRights(pidfd), nil, 0)
			if errors.Is(err, unix.EAGAIN) {
				_, err = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, int(controlTimeout/time.Millisecond)) //nolint:gosec // Linux file descriptors are signed int32.
				if err == nil {
					continue
				}
			}
			if !errors.Is(err, unix.EINTR) {
				return err
			}
		}
	}
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
			ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
			if !ok || f.Op == "stop" {
				err := tree.Stop(ctx)
				cancel()
				emit(resultFrame("empty", err))
				return 0
			}
			err := tree.Control(ctx, f.Op)
			cancel()
			reply := resultFrame(f.Op, err)
			if f.Op == "census" && err == nil {
				if c := tree.census.Load(); c != nil {
					reply.Census = *c
				}
			}
			emit(reply)
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
