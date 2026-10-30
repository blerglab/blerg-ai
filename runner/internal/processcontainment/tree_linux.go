//go:build linux

// Package processcontainment owns one kernel-traced descendant tree in a
// dedicated helper process. EXITKILL fences helper crashes; the inherited
// seccomp filter prevents CLONE_UNTRACED/clone3 escapes.
package processcontainment

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

var ErrUnproven = errors.New("process containment could not be proven")

// ErrTaskLimit means the tree passed its task limit (threads count) and was ended. The tree is still
// proven empty when Run returns it; callers report the reason instead of an unexplained kill.
var ErrTaskLimit = errors.New("process limit reached")

type request struct {
	op  string
	ctx context.Context
	ack chan error
}
type Tree struct {
	commands chan request
	done     chan struct{}
	err      error
	live     atomic.Int64
	tracked  atomic.Int64           // tasks the tracer accounts for; diagnostics and tests only
	census   atomic.Pointer[Census] // last census, taken at one instant of the tracer loop; diagnostics and tests only
	// Ledger, when set, receives a pidfd for every process of the tree before that process runs
	// its first instruction (the tracer continues no task whose process it could not record).
	// The callee owns nothing: Run closes the descriptor when Ledger returns.
	Ledger func(pidfd int) error
}

// announce records a newly tracked task's process in the ledger. A thread belongs to a process
// already recorded: its creator ran, so it was continued, so it was recorded.
func (t *Tree) announce(tid int) error {
	if t.Ledger == nil {
		return nil
	}
	fd, err := pidfdOpen(tid, 0)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOENT) {
		return nil // not a thread-group leader: EINVAL before Linux 6.16, ENOENT since
	}
	if err != nil {
		return fmt.Errorf("%w: pidfd for task %d: %w", ErrUnproven, tid, err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err = t.Ledger(fd); err != nil {
		return fmt.Errorf("%w: record task %d: %w", ErrUnproven, tid, err)
	}
	return nil
}

// pidfdOpen is pidfd_open(2). Tests replace it to answer as another kernel generation does.
var pidfdOpen = unix.PidfdOpen

func NewTree() *Tree { return &Tree{commands: make(chan request), done: make(chan struct{})} }
func (t *Tree) Control(ctx context.Context, op string) error {
	q := request{op: op, ctx: ctx, ack: make(chan error, 1)}
	select {
	case <-t.done:
		return t.err
	case t.commands <- q:
	case <-ctx.Done():
		return fmt.Errorf("%w: waiting for %s acknowledgement: %w", ErrUnproven, op, ctx.Err())
	}
	select {
	case err := <-q.ack:
		return err
	case <-t.done:
		return t.err
	case <-ctx.Done():
		return fmt.Errorf("%w: waiting for %s acknowledgement: %w", ErrUnproven, op, ctx.Err())
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
	if maximum < 1 {
		return fail(fmt.Errorf("%w: invalid traced task limit", ErrUnproven))
	}
	if e := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); e != nil {
		return fail(fmt.Errorf("%w: set child subreaper: %w", ErrUnproven, e))
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
		return fail(fmt.Errorf("%w: wait for initial trace stop: %w", ErrUnproven, e))
	}
	opts := unix.PTRACE_O_TRACEFORK | unix.PTRACE_O_TRACEVFORK | unix.PTRACE_O_TRACEVFORKDONE | unix.PTRACE_O_TRACECLONE | unix.PTRACE_O_TRACEEXEC | unix.PTRACE_O_TRACEEXIT | unix.PTRACE_O_EXITKILL
	if e := unix.PtraceSetOptions(root, opts); e != nil {
		_ = unix.Kill(root, unix.SIGKILL)
		_, _ = unix.Wait4(root, &status, 0, nil)
		return fail(fmt.Errorf("%w: set ptrace options: %w", ErrUnproven, e))
	}
	// Values retain the signal to deliver on thaw. -1 means still running or an
	// initial child stop not yet consumed. Never equate signal submission to freeze.
	tasks := map[int]int{root: -1}
	// An EXIT event commits this task to kernel exit; after continuation it cannot return to
	// user code. A zombie leader may not produce its final wait status until its siblings exit.
	// Keep it tracked, but do not wait for another signal-delivery stop to freeze it.
	exiting := map[int]bool{}
	// vforkWait maps a task that vforked to its child until the parent's VFORK_DONE: the kernel keeps
	// that thread blocked (uninterruptibly, so a SIGSTOP can't reach it) until the child execs or
	// exits. While the child is stopped the parent can run nothing, so it counts as frozen; without
	// this a freeze that lands between vfork and exec (every Go os/exec) never completes.
	vforkWait := map[int]int{}
	// held reports whether a task can run no code of its own: stopped, or blocked in vfork on a child
	// (or a chain of vfork children) that is.
	held := func(pid int) bool {
		for depth := 0; depth < 64; depth++ {
			if exiting[pid] {
				return true
			}
			if sig, ok := tasks[pid]; ok && sig >= 0 {
				return true
			}
			child, waiting := vforkWait[pid]
			if !waiting {
				return false
			}
			if _, alive := tasks[child]; !alive {
				return false
			}
			pid = child
		}
		return false
	}
	births := newBirths()
	frozen, killing, limitHit := false, false, false
	// dying classifies a failed ptrace request on a task held in a stop. ESRCH means the task left
	// that stop without being continued, which only a fatal signal does (SIGKILL, a group exit, a
	// sibling's exec): it runs no more user code, exactly as after its EXIT event, and its wait
	// status or EXEC event still settles its entry. An engine killing its own shells (a tool
	// timeout, an expired monitor) races the tracer this way. Any other failure ends the tree.
	dying := func(pid int, what string, e error) bool {
		if errors.Is(e, unix.ESRCH) {
			exiting[pid] = true
			return true
		}
		_, _ = fmt.Fprintf(os.Stderr, "process containment: %s task %d: %v; ending the tree\n", what, pid, e)
		return false
	}
	// end starts tearing the tree down for a reason that is not the caller's or the root's own: the
	// command then dies of SIGKILL (exit status 137), and this line is all that says why.
	end := func(reason string) {
		killing = true
		_, _ = fmt.Fprintf(os.Stderr, "process containment: %s; ending the tree\n", reason)
	}
	limitDetail := ""
	var pending *request
	var pendingSince time.Time
	var slowLogged, failureLogged bool
	diagnose := func(reason string) { logUnheldTasks(os.Stderr, reason, tasks, vforkWait, held) }
	defer func() {
		if pending != nil && err != nil {
			diagnose("failed: " + err.Error())
		}
	}()
	if e := t.announce(root); errors.Is(e, unix.ENOSYS) || errors.Is(e, unix.EPERM) {
		// No pidfds here (an old kernel or a container's seccomp profile): run without a ledger.
		// Its reader then holds no root record and never proves a lost helper's tree gone.
		t.Ledger = nil
	} else if e != nil {
		_ = unix.Kill(root, unix.SIGKILL)
		_, _ = unix.Wait4(root, &status, 0, nil)
		return fail(e)
	}
	ready <- nil
	if e := unix.PtraceCont(root, 0); e != nil {
		end(fmt.Sprintf("continue root task %d: %v", root, e))
	}
	for {
		t.tracked.Store(int64(len(tasks)))
		select {
		case q := <-t.commands:
			switch q.op {
			case "census":
				// One snapshot: the loop keeps collecting reports while frozen (tasks committed to exit
				// finish), so counters read after the ack may describe a later instant.
				live := kernelTraced()
				t.tracked.Store(int64(len(tasks)))
				t.live.Store(int64(len(live)))
				t.census.Store(takeCensus(tasks, live, vforkWait, exiting, births, held))
				q.ack <- nil
			case "freeze":
				if pending != nil || killing {
					q.ack <- fmt.Errorf("%w: freeze conflicts with pending control or tree teardown", ErrUnproven)
					break
				}
				frozen = true
				pending = &q
				pendingSince = time.Now()
				slowLogged, failureLogged = false, false
				// kill(2) targets a thread group even when given a TID: SIGSTOP
				// coalesces and leaves sibling threads running. Each traced
				// task needs its own signal-delivery stop before we can ack.
				for pid, sig := range tasks {
					if sig < 0 {
						_, _, _ = unix.Syscall(unix.SYS_TKILL, uintptr(pid), uintptr(unix.SIGSTOP), 0)
					}
				}
			case "thaw":
				if killing {
					q.ack <- fmt.Errorf("%w: thaw conflicts with tree teardown", ErrUnproven)
					break
				}
				if pending != nil {
					// A freeze that has not completed is cancelled, never left to complete unobserved:
					// tasks already stopped are continued below, and a stop still on its way is
					// continued when it arrives (frozen is false by then).
					if !failureLogged {
						diagnose("failed: freeze cancelled by thaw")
					}
					pending.ack <- fmt.Errorf("%w: freeze cancelled by thaw", ErrUnproven)
					pending = nil
				}
				frozen = false
				for pid, sig := range tasks {
					if sig >= 0 {
						if e := continueTask(pid, sig); e != nil && !dying(pid, "continue", e) {
							killing = true
						}
						tasks[pid] = -1
					}
				}
				if killing {
					q.ack <- fmt.Errorf("%w: ptrace continue failed during thaw", ErrUnproven)
				} else {
					q.ack <- nil
				}
			case "stop":
				killing = true
				q.ack <- nil
			default:
				q.ack <- fmt.Errorf("%w: unsupported tree control operation", ErrUnproven)
			}
		default:
		}
		if killing {
			for pid := range tasks {
				_ = unix.Kill(pid, unix.SIGKILL)
			}
		}
		if pending != nil {
			if !slowLogged && time.Since(pendingSince) >= 2*time.Second {
				diagnose("pending over 2s")
				slowLogged = true
			}
			if !failureLogged && pending.ctx.Err() != nil {
				diagnose("failed: " + pending.ctx.Err().Error())
				failureLogged = true
			}
			all := !killing
			for pid := range tasks {
				if !held(pid) {
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
				err := fmt.Errorf("%w: wait reported no children with %d tracked tasks remaining", ErrUnproven, len(tasks))
				if limitHit {
					err = errors.Join(err, fmt.Errorf("%w: more than %d tasks (threads count, %s)", ErrTaskLimit, maximum, limitDetail))
				}
				return code, err
			}
			if limitHit {
				return code, fmt.Errorf("%w: more than %d tasks (threads count, %s)", ErrTaskLimit, maximum, limitDetail)
			}
			return code, nil
		}
		if e != nil {
			for pid := range tasks {
				_ = unix.Kill(pid, unix.SIGKILL)
			}
			return code, fmt.Errorf("%w: wait for traced task: %w", ErrUnproven, e)
		}
		if pid == 0 {
			time.Sleep(time.Millisecond)
			continue
		}
		_, known := tasks[pid]
		initial := known && status.Stopped() && status.StopSignal() == unix.SIGSTOP && births.ambiguous(pid) && initialStop(pid)
		if births.reported(pid, known, initial) {
			if known {
				// The tracked task under this TID is gone (see births); this is a new task.
				delete(exiting, pid)
				delete(vforkWait, pid)
			}
			if status.Stopped() {
				if e := t.announce(pid); e != nil {
					end(e.Error())
				}
			}
		}
		if status.Exited() || status.Signaled() {
			delete(tasks, pid)
			delete(exiting, pid)
			delete(vforkWait, pid)
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
		if event == unix.PTRACE_EVENT_EXIT {
			exiting[pid] = true
		}
		if event == unix.PTRACE_EVENT_EXEC {
			// de_thread reuses the leader TID, including a zombie leader's. Its old EXIT proof
			// does not apply to the execing sibling. Until this EXEC stop that sibling is
			// independently tracked and must itself be held before freeze can acknowledge.
			delete(exiting, pid)
			previous, e := eventMessage(pid, unix.PTRACE_EVENT_EXEC)
			if e != nil {
				// Killed at its EXEC stop: its former TID is unknown, so nothing is dropped. A stale
				// entry it would have named is never held: a freeze fails closed, the tree end is unproven.
				if !dying(pid, "read the exec event of", e) {
					killing = true
				}
			} else if births.execed(int(previous)) {
				delete(tasks, int(previous))
				delete(exiting, int(previous))
				delete(vforkWait, int(previous))
			}
		}
		if event == unix.PTRACE_EVENT_VFORK_DONE {
			delete(vforkWait, pid)
		}
		if event == unix.PTRACE_EVENT_FORK || event == unix.PTRACE_EVENT_VFORK || event == unix.PTRACE_EVENT_CLONE {
			child, e := eventMessage(pid, event)
			if e != nil {
				// Killed at its creation event: the child, if it outlives it, is tracked from its own
				// first report (an untracked TID's report is a birth), announced before it runs.
				if !dying(pid, "read the creation event of", e) {
					killing = true
				}
			} else {
				// Record the vfork wait whichever arrives first: the child's initial stop can be
				// reported before the parent's VFORK event, so "seen" says nothing about it.
				_, seen := tasks[int(child)]
				if track, superseded := births.created(int(child), seen); track {
					if superseded {
						delete(exiting, int(child))
						delete(vforkWait, int(child))
					}
					if e := t.announce(int(child)); e != nil {
						end(e.Error())
					}
					tasks[int(child)] = -1
					seen = true
					if frozen {
						_, _, _ = unix.Syscall(unix.SYS_TKILL, uintptr(child), uintptr(unix.SIGSTOP), 0)
					}
				}
				// A child already reaped no longer holds its vfork parent.
				if event == unix.PTRACE_EVENT_VFORK && seen {
					vforkWait[pid] = int(child)
				}
			}
		}
		signal := int(status.StopSignal())
		if signal == int(unix.SIGTRAP) || signal == int(unix.SIGSTOP) {
			signal = 0
		}
		tasks[pid] = signal
		if len(tasks) > maximum && !limitHit {
			killing, limitHit = true, true
			limitDetail = fmt.Sprintf("tracked=%d live=%d", len(tasks), liveTraced(tasks))
			_, _ = fmt.Fprintf(os.Stderr, "process containment: task limit %d passed (%s); ending the tree\n", maximum, limitDetail)
		}
		// EXIT stops must be continued even during teardown: SIGKILL cannot release a
		// task already committed to exit. It can finish only kernel exit work, not user code.
		if event == unix.PTRACE_EVENT_EXIT || (!frozen && !killing) {
			if e := continueTask(pid, signal); e != nil && !dying(pid, "continue", e) {
				killing = true
			}
			tasks[pid] = -1
		}
	}
}

// continueTask resumes a task from a ptrace stop. Tests replace it to kill the task first.
var continueTask = unix.PtraceCont

// readEventMessage is PTRACE_GETEVENTMSG. Tests replace it to kill the task first.
var readEventMessage = unix.PtraceGetEventMsg

// eventMessage reads the message of the event stop tid was reported in. A fatal signal can end that
// stop and take the task into its EXIT stop before the read, whose message (an exit status, such as
// 9 for SIGKILL) would then be taken for a TID: tracked forever, or deleted as an exec's former TID.
// So the stop is confirmed after the read. A task leaves its EXIT stop, or reaches another event
// stop, only when the tracer continues it, so a confirmed stop proves the message is its own. An
// unconfirmed one is reported as ESRCH: the task left the stop and is dying.
func eventMessage(tid, event int) (uint, error) {
	msg, err := readEventMessage(tid)
	if err != nil {
		return 0, err
	}
	code, err := stopCode(tid)
	if err != nil {
		return 0, err
	}
	if int(code) != int(unix.SIGTRAP)|event<<8 {
		return 0, unix.ESRCH
	}
	return msg, nil
}

// stopCode is the si_code of the stop tid is in; for an event stop, SIGTRAP | event<<8.
func stopCode(tid int) (int32, error) {
	var info [128]byte
	//nolint:gosec // fixed-size siginfo buffer that outlives the call
	if _, _, e := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_GETSIGINFO, uintptr(tid), 0, uintptr(unsafe.Pointer(&info[0])), 0, 0); e != 0 {
		return 0, e
	}
	return int32(binary.NativeEndian.Uint32(info[8:])), nil //nolint:gosec // si_code is a C int
}

// births keeps the tracer's TID-keyed accounting tied to kernel tasks, not to reused numbers.
//
// wait4 returns a creator's FORK/VFORK/CLONE event and the child's own reports in either order: the
// child's initial stop, and even its exit status, may come first. Adding a child at its creator's
// event after its exit was collected would track a task that no longer exists. early counts such
// reports per TID, since one TID can be reported, reaped and reused before either event arrives.
//
// A tracked TID can also go stale. When a non-leader thread T execs, de_thread frees T's TID before
// the tracer collects the EXEC event that names it, and a new task C may get that number first. A
// new task's first report is always its auto-attach SIGSTOP, queued without siginfo, so it reads
// as SI_USER from PID 0 (initialStop); the tracer's own freeze stops are SI_TKILL from the helper.
// Such a stop under a tracked, non-pending TID therefore supersedes the stale entry: C is a birth
// (recorded in the ledger before it runs), and the pending EXEC must not delete C's entry. A
// creator event naming a tracked TID that neither reported early nor is pending is stale the same
// way. stale counts superseded entries per TID for their EXEC events to consume.
//
// The SI_USER/PID 0 signature can also come from a SIGSTOP sent while the signal queue limit is
// exhausted or from an ancestor PID namespace. Misreading a live task as reborn only ever
// re-records it and leaves an extra stale count, which can keep a later stale entry: a freeze
// then waits for it and fails closed. It never drops a live task.
type births struct {
	early   map[int]int  // reports that came before their creator's event
	pending map[int]bool // tracked from the creator's event; the child's own first report is due
	stale   map[int]int  // superseded entries whose EXEC event is still to come
}

func newBirths() *births {
	return &births{early: map[int]int{}, pending: map[int]bool{}, stale: map[int]int{}}
}

// ambiguous reports whether a SIGSTOP under this tracked TID could be a new task's first stop.
func (b *births) ambiguous(tid int) bool { return !b.pending[tid] }

// reported handles a report for tid (known: tracked; initial: an auto-attach SIGSTOP) and says
// whether it is a new task's first report.
func (b *births) reported(tid int, known, initial bool) bool {
	switch {
	case !known:
		b.early[tid]++
		return true
	case b.pending[tid]:
		delete(b.pending, tid)
		return false
	case initial:
		b.stale[tid]++
		b.early[tid]++
		return true
	}
	return false
}

// created consumes a creation event for tid (known: tracked). track says the child is still to
// report itself and must be tracked as pending; superseded says that replaces a stale entry.
func (b *births) created(tid int, known bool) (track, superseded bool) {
	if b.early[tid] > 0 {
		b.early[tid]--
		if b.early[tid] == 0 {
			delete(b.early, tid)
		}
		return false, false
	}
	if known && b.pending[tid] {
		return false, false
	}
	b.pending[tid] = true
	if known {
		b.stale[tid]++
		return true, true
	}
	return true, false
}

// execed reports whether an EXEC event's former TID still names the execing thread's entry.
func (b *births) execed(previous int) bool {
	if b.stale[previous] > 0 {
		b.stale[previous]--
		if b.stale[previous] == 0 {
			delete(b.stale, previous)
		}
		return false
	}
	delete(b.pending, previous)
	return true
}

// initialStop reports whether tid's current SIGSTOP stop carries the siginfo of the auto-attach
// stop: SI_USER from PID 0, with no queued sender.
func initialStop(tid int) bool {
	var info [128]byte
	//nolint:gosec // fixed-size siginfo buffer that outlives the call
	if _, _, e := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_GETSIGINFO, uintptr(tid), 0, uintptr(unsafe.Pointer(&info[0])), 0, 0); e != 0 {
		return false
	}
	// si_signo, si_errno, si_code, then the union, aligned to a pointer: si_pid, si_uid.
	pidOffset := 8 + unsafe.Sizeof(uintptr(0))
	code := binary.NativeEndian.Uint32(info[8:])
	sender := binary.NativeEndian.Uint32(info[pidOffset:])
	return code == 0 && sender == 0 // SI_USER is 0
}

func (t *Tree) Stop(ctx context.Context) error {
	if err := t.Control(ctx, "stop"); err != nil {
		return err
	}
	select {
	case <-t.done:
		return t.err
	case <-ctx.Done():
		return fmt.Errorf("%w: waiting for tree exit after stop: %w", ErrUnproven, ctx.Err())
	}
}

// Diagnostics never supply containment evidence. Bound the status reads and omit command lines.
func logUnheldTasks(w io.Writer, reason string, tasks, vforkWait map[int]int, held func(int) bool) {
	const limit = 32
	ids := make([]int, 0, len(tasks))
	for tid := range tasks {
		if !held(tid) {
			ids = append(ids, tid)
		}
	}
	sort.Ints(ids)
	_, _ = fmt.Fprintf(w, "process containment freeze %s: unheld=%d (showing at most %d)\n", reason, len(ids), limit)
	for i, tid := range ids {
		if i == limit {
			break
		}
		child, waiting := vforkWait[tid]
		_, _ = fmt.Fprintf(w, "process containment freeze: tid=%d tasks=%d vforkWait=%d present=%t %s\n", tid, tasks[tid], child, waiting, taskStatus(tid))
	}
}

// liveTraced counts tracked tasks the kernel still reports as traced by this thread. Diagnostic only:
// it tells a real runaway tree from tracer accounting that kept tasks which no longer exist.
func liveTraced(tasks map[int]int) int {
	want := "TracerPid:\t" + strconv.Itoa(unix.Gettid()) + "\n"
	n := 0
	for tid := range tasks {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", tid))
		if err == nil && strings.Contains(string(data), want) {
			n++
		}
	}
	return n
}
func taskStatus(tid int) string {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", tid))
	if os.IsNotExist(err) {
		return "gone"
	}
	if err != nil {
		return "status unavailable"
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 8192))
	if err != nil {
		return "status unreadable"
	}
	return statusFields(string(data))
}
func statusFields(data string) string {
	fields := []string{}
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && (key == "State" || key == "Tgid" || key == "TracerPid" || key == "PPid" || key == "SigPnd" || key == "ShdPnd") {
			fields = append(fields, key+"="+strings.TrimSpace(value))
		}
	}
	return strings.Join(fields, " ")
}

// kernelTraced independently enumerates all tasks owned by this tracer thread, with their status
// lines. Only the explicit diagnostic request pays for the proc scan.
func kernelTraced() map[int]string {
	want := "TracerPid:\t" + strconv.Itoa(unix.Gettid()) + "\n"
	paths, _ := filepath.Glob("/proc/[0-9]*/task/[0-9]*/status")
	live := map[int]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // a /proc status path from the fixed glob above
		if err == nil && strings.Contains(string(data), want) {
			tid, _ := strconv.Atoi(filepath.Base(filepath.Dir(path)))
			live[tid] = statusFields(string(data))
		}
	}
	return live
}

// takeCensus compares the tracer's accounting with the kernel's and names every task they disagree
// on. The /proc scan is not atomic: a task committed to exit may vanish during it. A live task the
// tracer does not account for has no such excuse. Diagnostic only.
func takeCensus(tasks map[int]int, live map[int]string, vforkWait map[int]int, exiting map[int]bool, b *births, held func(int) bool) *Census {
	c := &Census{Tracked: int64(len(tasks)), Live: int64(len(live))}
	var lines []string
	for tid, status := range live {
		if _, ok := tasks[tid]; !ok {
			c.Untracked++
			lines = append(lines, fmt.Sprintf("untracked tid=%d early=%d %s", tid, b.early[tid], status))
		}
	}
	for tid, sig := range tasks {
		if _, ok := live[tid]; !ok {
			if exiting[tid] {
				c.Exited++
			} else {
				c.Lost++
			}
			child, waiting := vforkWait[tid]
			lines = append(lines, fmt.Sprintf("not traced tid=%d sig=%d held=%t exiting=%t pending=%t vforkWait=%d/%t %s", tid, sig, held(tid), exiting[tid], b.pending[tid], child, waiting, taskStatus(tid)))
		}
	}
	if c.Untracked+c.Lost != 0 {
		for tid, sig := range tasks {
			child, waiting := vforkWait[tid]
			lines = append(lines, fmt.Sprintf("tracked tid=%d sig=%d held=%t exiting=%t pending=%t vforkWait=%d/%t %s", tid, sig, held(tid), exiting[tid], b.pending[tid], child, waiting, live[tid]))
		}
	}
	sort.Strings(lines)
	c.Mismatch = strings.Join(lines, "; ")
	return c
}
