//go:build linux

// Package processcontainment owns one kernel-traced descendant tree in a
// dedicated helper process. EXITKILL fences helper crashes; the inherited
// seccomp filter prevents CLONE_UNTRACED/clone3 escapes. See
// docs/design/sound-process-freeze.md for why a freeze holds.
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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/blerglab/blerg-ai/runner/internal/processtelemetry"
	"golang.org/x/sys/unix"
)

var ErrUnproven = errors.New("process containment could not be proven")

// ErrStart means the command never started: it could not be executed, or could not be traced.
var ErrStart = errors.New("traced command could not start")

// rootTaskReserve is how far past the limit the root's own process may go, and no descendant can. It
// is what lets the engine start a tool in a full tree.
const rootTaskReserve = 64

// rootThreadCeiling is where the root's threads are refused. A refused thread is fatal to many programs
// (Go aborts), and the root is the engine or the person's shell: it must not lose one to any tree, its
// own children's included. Every other task stops at maximum+rootTaskReserve, so at this ceiling the
// root's process alone holds at least maximum threads.
func rootThreadCeiling(maximum int) int { return 2*maximum + rootTaskReserve }

// limitNoticeEvery spaces the notices of one tree at its limit. Tests shorten it.
var limitNoticeEvery = time.Minute

// engineFirstFor is how long after a refusal the tracer asks for the reports of the root's own tasks
// before any other's. wait(-1) takes the first report in the kernel's list, and at the limit the tasks
// ahead keep having one, so a thread the engine just started would wait seconds at each stop.
const engineFirstFor = 100 * time.Millisecond

// reportSpin is how long the tracer polls for the next report before it sleeps. A measurement varies it.
var reportSpin = 100 * time.Microsecond

// barrierPause bounds pausing a tree for a non-leader exec. A pause that takes longer is given up,
// the tree runs on, and it is tried again barrierRetry later; only the exec waits. Tests shorten them.
var barrierPause, barrierRetry = 10 * time.Second, time.Second

type request struct {
	op  string
	ctx context.Context
	ack chan error
}
type Tree struct {
	activity *activityQueue
	commands chan request
	done     chan struct{}
	err      error
	live     atomic.Int64
	tracked  atomic.Int64           // tasks the tracer accounts for; diagnostics and tests only
	reborn   atomic.Int64           // creator events naming a task still tracked; the barrier keeps this at zero
	bypassed atomic.Int64           // execs that changed a thread's TID with no barrier step; the filter keeps this at zero
	barriers atomic.Int64           // non-leader execs stepped alone; diagnostics and tests only
	retries  atomic.Int64           // pauses given up; diagnostics and tests only
	refused  atomic.Int64           // creations refused at the limit; diagnostics and tests only
	census   atomic.Pointer[Census] // last census, taken at one instant of the tracer loop; diagnostics and tests only
	// Ledger, when set, receives a pidfd for every process of the tree before that process runs
	// its first instruction (the tracer continues no task whose process it could not record).
	// The callee owns nothing: Run closes the descriptor when Ledger returns.
	Ledger func(pidfd int) error

	// atLimit signals that a creation was refused. See AtLimit.
	atLimit chan struct{}

	// Tracer thread only. pidfds holds each process's descriptor by leader TID, for teardown; threads
	// names the non-leader tasks; noPidfds means this system has no pidfds at all.
	pidfds   map[int]int
	threads  map[int]bool
	noPidfds bool
}

// announce notes a newly tracked task and records its process in the ledger. A thread belongs to a
// process already recorded: its creator ran, so it was continued, so it was recorded.
func (t *Tree) announce(tid int) error {
	delete(t.threads, tid)
	if t.noPidfds {
		t.classify(tid)
		return nil
	}
	fd, err := pidfdOpen(tid, 0)
	if errors.Is(err, unix.EMFILE) || errors.Is(err, unix.ENFILE) {
		// Teardown can do without them (see signalTree); the ledger's record cannot.
		t.closePidfds()
		fd, err = pidfdOpen(tid, 0)
	}
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOENT) {
		t.threads[tid] = true // not a thread-group leader: EINVAL before Linux 6.16, ENOENT since
		return nil
	}
	if err != nil && t.Ledger == nil && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EPERM) {
		t.classify(tid)
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: pidfd for task %d: %w", ErrUnproven, tid, err)
	}
	if t.Ledger != nil {
		if err = t.Ledger(fd); err != nil {
			_ = unix.Close(fd)
			return fmt.Errorf("%w: record task %d: %w", ErrUnproven, tid, err)
		}
	}
	t.pidfds[tid] = fd
	return nil
}

func (t *Tree) closePidfds() {
	for tid, fd := range t.pidfds {
		_ = unix.Close(fd)
		delete(t.pidfds, tid)
	}
}

// classify tells a thread from a leader with no pidfd to ask. Only while tid is known to be this new
// task: later, the number could be anyone's. A task that can't be read counts as a thread, the safe
// answer both for the barrier and for teardown.
func (t *Tree) classify(tid int) {
	// #nosec G703 -- tid is a kernel task number formatted as a decimal integer under /proc.
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", tid))
	if err != nil || !strings.Contains(string(data), fmt.Sprintf("\nTgid:\t%d\n", tid)) {
		t.threads[tid] = true
	}
}

// signalTree sends SIGKILL to every process with a tracked task: through its pidfd, or by number for
// a leader, whose number stays with its process until the tracer reaps it, across any exec. A thread's
// number is never signaled: its exec frees the number before the EXEC event drops the entry, and the
// barrier's pause holds only this tree, so a process outside it can already own that number. The
// thread goes with its process, whose leader stays tracked for as long as any thread lives.
func (t *Tree) signalTree(tasks map[int]int) {
	for tid := range tasks {
		if fd, ok := t.pidfds[tid]; ok {
			_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		} else if !t.threads[tid] {
			_ = unix.Kill(tid, unix.SIGKILL)
		}
	}
}

// unproven says why no freeze of this tree can be trusted any more, if something has made it so.
func (t *Tree) unproven() error {
	switch {
	case t.bypassed.Load() != 0:
		return fmt.Errorf("%w: a thread's exec passed the exec barrier", ErrUnproven)
	case t.reborn.Load() != 0:
		return fmt.Errorf("%w: a tracked task's TID was reused", ErrUnproven)
	}
	return nil
}

// threadGroup is the process a task belongs to, or 0 if it can't be read. Only while tid is known to
// be that task, as for classify.
func threadGroup(tid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", tid))
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(data)) {
		if value, ok := strings.CutPrefix(line, "Tgid:"); ok {
			tgid, _ := strconv.Atoi(strings.TrimSpace(value))
			return tgid
		}
	}
	return 0
}

// pidfdOpen is pidfd_open(2). Tests replace it to answer as another kernel generation does.
var pidfdOpen = unix.PidfdOpen

func NewTree() *Tree {
	return &Tree{commands: make(chan request), done: make(chan struct{}), atLimit: make(chan struct{}, 1), pidfds: map[int]int{}, threads: map[int]bool{}}
}

// AtLimit signals that the tree reached its limit and a new task was refused, at most once in
// limitNoticeEvery, so the caller can say so. The tracer never waits for a reader.
func (t *Tree) AtLimit() <-chan struct{} { return t.atLimit }

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

// Run must execute on the helper's sole child-reaper thread, and its goroutine's thread is the
// tracer. start starts cmd, without SysProcAttr.Ptrace (the tree is seized) and optionally on a PTY.
// ready means the kernel owns every descendant before its first instruction, not merely that Start
// returned: it comes at the root's exec of the command, and a command that cannot run reports ErrStart.
// A task that would pass maximum is refused with EAGAIN and the tree goes on (see
// docs/design/process-limit-refusal.md); the count never ends a tree.
func (t *Tree) Run(cmd *exec.Cmd, start func() error, maximum int, ready chan<- error) (code int, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() { t.err = err; close(t.done) }()
	defer t.closePidfds()
	if t.activity != nil {
		defer t.activity.close()
	}
	started := false
	fail := func(e error) (int, error) { started = true; ready <- e; return 1, e }
	if maximum < 1 {
		return fail(fmt.Errorf("%w: invalid traced task limit", ErrUnproven))
	}
	if e := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); e != nil {
		return fail(fmt.Errorf("%w: set child subreaper: %w", ErrUnproven, e))
	}
	if e := InstallFilter(); e != nil {
		return fail(e)
	}
	throughShim(cmd)
	if e := start(); e != nil {
		return fail(e)
	}
	root := cmd.Process.Pid
	defer func() { _ = cmd.Process.Release() }()
	if e := t.seizeRoot(root); e != nil {
		_ = unix.Kill(root, unix.SIGKILL)
		var status unix.WaitStatus
		_, _ = unix.Wait4(root, &status, unix.WALL, nil)
		return fail(e)
	}
	defer func() {
		if !started { // ended before the root's exec
			ready <- err
		}
	}()
	// Values retain the signal to deliver on thaw. -1 means running, or a new task not yet reported.
	// Never equate a request to freeze with a task being frozen.
	tasks := map[int]int{root: -1}
	// An EXIT event commits this task to kernel exit; after continuation it cannot return to
	// user code. A zombie leader may not produce its final wait status until its siblings exit.
	// Keep it tracked, but do not wait for another stop to freeze it.
	exiting := map[int]bool{}
	// vforkWait maps a task that vforked to its child until the parent's VFORK_DONE: the kernel keeps
	// that thread blocked (uninterruptibly, so PTRACE_INTERRUPT can't stop it) until the child execs
	// or exits. While the child is stopped the parent can run nothing, so it counts as frozen; without
	// this a freeze that lands between vfork and exec (every Go os/exec) never completes.
	vforkWait := map[int]int{}
	// listening holds the tasks in a group stop of the tree's own (SIGSTOP, ^Z): parked with
	// PTRACE_LISTEN, they run nothing, and SIGCONT makes them report again. A thaw leaves them there.
	listening := map[int]bool{}
	// execs holds the non-leader tasks stopped at the entry of an exec, each waiting for its barrier.
	// stepping is the one going through its exec alone, in a paused tree: when a non-leader execs,
	// de_thread frees its TID before the EXEC event can be collected, so nothing may be allowed to
	// allocate a TID until it has been (see the design).
	execs := map[int]bool{}
	stepping, steppedLeader, pausing := 0, 0, false
	// admitted holds the tasks let into a creating syscall, each reserving the place of the task it may
	// create until its creation event or syscall-exit stop says how that ended, or until it dies. They
	// run on with PTRACE_SYSCALL for that exit stop. tasks plus admitted is what the limit compares.
	admitted := map[int]bool{}
	// own holds the tasks known to be in the root's process (see engineFirstFor).
	own := map[int]bool{root: true}
	var noticedAt, refusedAt time.Time
	var pausedAt, stepAt, retryAt time.Time
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
	allHeld := func() bool {
		for pid := range tasks {
			if !held(pid) {
				return false
			}
		}
		return true
	}
	births := newBirths()
	frozen, killing := false, false
	// A fatal signal stays pending until delivered. Queue each newly recorded task
	// once, including births discovered during teardown; resending to the whole
	// shrinking tree at every wait event makes teardown quadratic at the limit.
	killPending := map[int]int{root: 0}
	announce := func(pid int) error {
		killPending[pid] = 0
		return t.announce(pid)
	}
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
	// interrupt stops every running task. It is not a signal: SIGCONT cannot undo it, and it reaches
	// only tracees of this thread. A task it cannot reach is on its way out and reports that.
	interrupt := func() {
		for pid, sig := range tasks {
			if sig < 0 {
				_ = unix.PtraceInterrupt(pid)
			}
		}
	}
	// resume continues a stopped task from its held state.
	resume := func(pid int) {
		if listening[pid] || execs[pid] {
			return
		}
		proceed := continueTask
		if admitted[pid] {
			proceed = createTask
		}
		if e := proceed(pid, tasks[pid]); e != nil && !dying(pid, "continue", e) {
			killing = true
		}
		tasks[pid] = -1
	}
	resumeAll := func() {
		for pid, sig := range tasks {
			if sig >= 0 {
				resume(pid)
			}
		}
	}
	endStep := func() {
		stepping = 0
		if !frozen && !killing {
			resumeAll()
		}
	}
	// drop forgets a thread whose exec moved it to its leader's number.
	drop := func(former int) {
		delete(tasks, former)
		delete(killPending, former)
		delete(exiting, former)
		delete(vforkWait, former)
		delete(admitted, former)
		delete(own, former)
		delete(t.threads, former)
		births.execed(former)
	}
	// admit decides a creating syscall, with its caller held at the stop.
	admit := func(pid int) {
		used := len(tasks) + len(admitted)
		allowed := maximum
		// A thread's report can beat its creator's event, so own can be late. A leader that is not the
		// root is another process, which is every task of a fork bomb: no reading for those.
		if used >= maximum && (own[pid] || (t.threads[pid] && threadGroup(pid) == root)) {
			allowed += rootTaskReserve
			if used >= allowed {
				thread, e := creatingThread(pid)
				if e != nil {
					if !dying(pid, "read the creation of", e) {
						killing = true
					}
					return
				}
				if thread {
					allowed = rootThreadCeiling(maximum)
				}
			}
		}
		if used < allowed {
			admitted[pid] = true
			return
		}
		if e := refuseTask(pid, unix.EAGAIN); e != nil {
			if !dying(pid, "refuse a new task of", e) {
				killing = true
			}
			return
		}
		t.refused.Add(1)
		refusedAt = time.Now()
		if refusedAt.Sub(noticedAt) >= limitNoticeEvery {
			noticedAt = refusedAt
			select {
			case t.atLimit <- struct{}{}:
			default:
			}
		}
	}
	var pending *request
	var pendingSince time.Time
	var slowLogged, failureLogged bool
	diagnose := func(reason string) { logUnheldTasks(os.Stderr, reason, tasks, vforkWait, held) }
	defer func() {
		if pending != nil && err != nil {
			diagnose("failed: " + err.Error())
		}
	}()
	var status unix.WaitStatus
	var lastReport time.Time
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
				census := takeCensus(tasks, live, vforkWait, exiting, births, held)
				census.Reserved = int64(len(admitted))
				t.census.Store(census)
				q.ack <- nil
			case "freeze":
				if pending != nil || killing {
					q.ack <- fmt.Errorf("%w: freeze conflicts with pending control or tree teardown", ErrUnproven)
					break
				}
				if e := t.unproven(); e != nil {
					q.ack <- e
					break
				}
				frozen, pausing = true, false
				pending = &q
				pendingSince = time.Now()
				slowLogged, failureLogged = false, false
				interrupt()
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
				if stepping == 0 && !pausing { // a barrier resumes the tree when it is done
					resumeAll()
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
			t.signalTree(killPending)
			clear(killPending)
		}
		if stepping != 0 && !killing && time.Since(stepAt) >= controlTimeout {
			end(fmt.Sprintf("the exec of task %d did not finish in a paused tree", stepping))
		}
		if len(execs) != 0 && stepping == 0 && !frozen && !killing {
			now := time.Now()
			if !pausing && now.After(retryAt) {
				pausing, pausedAt = true, now
				interrupt()
			}
			if pausing && allHeld() {
				pausing = false
				for pid := range execs {
					delete(execs, pid)
					stepping, stepAt = pid, now
					steppedLeader = threadGroup(pid)
					t.barriers.Add(1)
					// Its next report is its EXEC event, its syscall-exit stop (the exec failed) or its death.
					if e := stepTask(pid, 0); e != nil {
						stepping = 0
						if !dying(pid, "step the exec of", e) {
							killing = true
						}
					} else {
						tasks[pid] = -1
					}
					break
				}
				if stepping == 0 {
					resumeAll()
				}
			} else if pausing && now.Sub(pausedAt) >= barrierPause {
				pausing, retryAt = false, now.Add(barrierRetry)
				t.retries.Add(1)
				resumeAll()
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
			if e := t.unproven(); e != nil {
				// Found while the freeze was landing. Nothing it stopped stays held for a thaw to come.
				pending.ack <- e
				pending, frozen = nil, false
				if stepping == 0 && !pausing && !killing {
					resumeAll()
				}
			} else if !killing && allHeld() {
				pending.ack <- nil
				pending = nil
			}
		}
		var pid int
		var e error
		if time.Since(refusedAt) < engineFirstFor && !killing {
			for tid := range own {
				if got, failed := waitTask(tid, &status, unix.WNOHANG|unix.WALL, nil); failed == nil && got == tid {
					pid = tid
					break
				}
			}
		}
		if pid == 0 {
			pid, e = waitTask(-1, &status, unix.WNOHANG|unix.WALL, nil)
		}
		if errors.Is(e, unix.EINTR) {
			continue
		}
		if errors.Is(e, unix.ECHILD) {
			if len(tasks) != 0 {
				return code, fmt.Errorf("%w: wait reported no children with %d tracked tasks remaining", ErrUnproven, len(tasks))
			}
			return code, nil
		}
		if e != nil {
			t.signalTree(tasks)
			return code, fmt.Errorf("%w: wait for traced task: %w", ErrUnproven, e)
		}
		if pid == 0 {
			// A tracee usually reports again within microseconds of a continue. Every stop of a
			// build's fork, exec and exit would otherwise wait out a sleep.
			if time.Since(lastReport) >= reportSpin {
				time.Sleep(time.Millisecond)
			}
			continue
		}
		lastReport = time.Now()
		_, known := tasks[pid]
		if known && !t.noPidfds && t.threads[pid] && !births.pending[pid] && status.Stopped() && status.TrapCause() == unix.PTRACE_EVENT_STOP {
			// Past the barrier a thread's freed number can be a new process's, and that process's first
			// report can beat both its creator's event and the EXEC event that would show this entry
			// stale. Its first report is always this stop. Only a thread's number can change hands, and
			// a thread announced again is still a thread, so asking here costs one failed pidfd_open.
			if e := announce(pid); e != nil {
				end(e.Error())
			}
			if !t.threads[pid] {
				t.reborn.Add(1)
				_, _ = fmt.Fprintf(os.Stderr, "process containment: process %d reported under a tracked thread's TID; freezes will fail\n", pid)
				delete(exiting, pid)
				delete(vforkWait, pid)
				births.early[pid]++ // recorded now: its creator's event has nothing left to do
			}
		} else if births.reported(pid, known) && status.Stopped() {
			if e := announce(pid); e != nil {
				end(e.Error())
			}
		}
		if status.Exited() || status.Signaled() {
			t.activity.publish(processtelemetry.Exit, pid, 0, lastReport)
			delete(tasks, pid)
			delete(killPending, pid)
			delete(exiting, pid)
			delete(vforkWait, pid)
			delete(listening, pid)
			delete(execs, pid)
			delete(admitted, pid)
			delete(own, pid)
			delete(t.threads, pid)
			if fd, ok := t.pidfds[pid]; ok {
				_ = unix.Close(fd)
				delete(t.pidfds, pid)
			}
			switch {
			case pid == stepping:
				endStep()
			case stepping != 0 && pid == steppedLeader:
				// Killed between de_thread and its EXEC stop: a fatal signal skips that stop, and the
				// task exited under its leader's number. A leader's status comes after its threads', so
				// the step's entry names nothing now.
				drop(stepping)
				endStep()
			}
			if pid == root {
				if !started {
					return fail(startFailure(status))
				}
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
		event := int(status >> 16)
		delete(listening, pid)
		if event == unix.PTRACE_EVENT_EXIT {
			exiting[pid] = true
			delete(execs, pid)
			delete(admitted, pid)
		}
		if event == 0 && status.StopSignal() == unix.SIGTRAP|0x80 {
			delete(admitted, pid) // the exit stop of a creation that failed: no creation event came
		}
		if event == unix.PTRACE_EVENT_SECCOMP {
			// From the registers: a filter of the tree's own that also answers TRACE can set the event's data.
			if call, e := stoppedSyscall(pid); e != nil {
				if !dying(pid, "read the syscall of", e) {
					killing = true
				}
			} else if slices.Contains(creators, call) {
				admit(pid)
			} else if t.threads[pid] {
				execs[pid] = true
			}
		}
		stepDone := pid == stepping
		if event == unix.PTRACE_EVENT_EXEC {
			t.activity.publish(processtelemetry.Exec, pid, 0, lastReport)
			// de_thread reuses the leader TID, including a zombie leader's. Its old EXIT proof
			// does not apply to the execing sibling. Until this EXEC stop that sibling is
			// independently tracked and must itself be held before freeze can acknowledge. A creation
			// of the old leader's ended with it.
			delete(exiting, pid)
			delete(admitted, pid)
			previous, e := eventMessage(pid, unix.PTRACE_EVENT_EXEC)
			if e != nil {
				// Killed at its EXEC stop. A stepped exec is the stepped task's; any other's former TID is
				// unknown, so nothing is dropped: a stale entry is never held, and a freeze fails closed.
				if !dying(pid, "read the exec event of", e) {
					killing = true
				}
				if stepping != 0 {
					previous, stepDone = uint(stepping), true
				}
			}
			former := int(previous)
			if former != pid && former != 0 {
				// The exec took the leader's TID. The former one gets no exit report of its own.
				t.activity.publish(processtelemetry.Exit, former, 0, lastReport)
			}
			switch {
			case e != nil && !stepDone, former == pid:
			case former == stepping:
				stepDone = true
				drop(former)
			default:
				// Past the barrier: the tree ran between de_thread and this report, so the number may
				// be another task's by now. Nothing proves what happened in between, so no later
				// freeze is acknowledged. What the kernel says now still decides the entry: a tracee
				// under that number is one of ours and stays tracked, anything else is not this tree's.
				if t.bypassed.Add(1) == 1 {
					_, _ = fmt.Fprintf(os.Stderr, "process containment: thread %d exec'd past the exec barrier; freezes will fail\n", former)
				}
				if _, tracked := tasks[former]; tracked && liveTraced(map[int]int{former: 0}) != 0 {
					if t.threads[former] { // otherwise its own first report recorded it
						if e := announce(former); e != nil {
							end(e.Error())
						}
						births.kept(former)
					}
				} else {
					drop(former)
				}
			}
			if !started && pid == root {
				started = true
				ready <- nil
			}
		}
		if event == unix.PTRACE_EVENT_VFORK_DONE {
			delete(vforkWait, pid)
		}
		if event == unix.PTRACE_EVENT_FORK || event == unix.PTRACE_EVENT_VFORK || event == unix.PTRACE_EVENT_CLONE {
			delete(admitted, pid) // its place passes to the child's entry
			child, e := eventMessage(pid, event)
			if e != nil {
				// Killed at its creation event: the child, if it outlives it, is tracked from its own
				// first report (an untracked TID's report is a birth), announced before it runs.
				if !dying(pid, "read the creation event of", e) {
					killing = true
				}
			} else {
				t.activity.publish(processtelemetry.Fork, int(child), pid, lastReport)
				// Record the vfork wait whichever arrives first: the child's initial stop can be
				// reported before the parent's VFORK event, so "seen" says nothing about it.
				_, seen := tasks[int(child)]
				track, reborn := births.created(int(child), seen)
				if reborn {
					// Only an exec past the barrier frees a tracked TID. This entry stood for another task,
					// and no later freeze can be trusted. It need not end the tree.
					t.reborn.Add(1)
					_, _ = fmt.Fprintf(os.Stderr, "process containment: task %d created under a tracked TID; freezes will fail\n", child)
					delete(exiting, int(child))
					delete(vforkWait, int(child))
				}
				if track {
					if e := announce(int(child)); e != nil {
						end(e.Error())
					}
					tasks[int(child)] = -1
					seen = true
				}
				if event == unix.PTRACE_EVENT_CLONE && own[pid] && threadGroup(int(child)) == root {
					own[int(child)] = true
				}
				// A child already reaped no longer holds its vfork parent.
				if event == unix.PTRACE_EVENT_VFORK && seen {
					vforkWait[pid] = int(child)
				}
			}
		}
		// A signal to deliver on continuing. An event stop has none, and neither has a syscall stop
		// (SIGTRAP|0x80). The stop signals pass like any other: the kernel then starts a group stop.
		signal := int(status.StopSignal())
		if event != 0 || signal == int(unix.SIGTRAP) || signal == int(unix.SIGTRAP|0x80) {
			signal = 0
		}
		tasks[pid] = signal
		if event == unix.PTRACE_EVENT_STOP && status.StopSignal() != unix.SIGTRAP {
			// The tree's own group stop. Stopped is what it asked for: frozen or not, it stays parked.
			if e := ptraceListen(pid); e == nil {
				listening[pid] = true
			} else if !dying(pid, "listen to", e) {
				killing = true
			}
			continue
		}
		if stepDone {
			endStep()
			if !frozen && !killing {
				continue // resumed with the rest, this task included
			}
		}
		// EXIT stops must be continued even during teardown: SIGKILL cannot release a
		// task already committed to exit. It can finish only kernel exit work, not user code.
		if event == unix.PTRACE_EVENT_EXIT || (!frozen && !killing && !pausing && stepping == 0) {
			resume(pid)
		}
	}
}

// seizeRoot takes the shim at its self-stop, recording it before anything can run. Stopped means the
// tracer holds the only task that matters; if it never stops, or SIGCONT frees it first, its exec fails.
func (t *Tree) seizeRoot(root int) error {
	var status unix.WaitStatus
	wait := func(options int) error {
		for {
			if _, e := unix.Wait4(root, &status, options, nil); !errors.Is(e, unix.EINTR) {
				return e
			}
		}
	}
	if e := wait(unix.WUNTRACED); e != nil {
		return fmt.Errorf("%w: wait for the root to stop: %w", ErrStart, e)
	}
	if !status.Stopped() {
		return startFailure(status)
	}
	if e := ptraceSeize(root, traceOptions); e != nil {
		return fmt.Errorf("%w: seize the root: %w", ErrStart, e)
	}
	if e := wait(unix.WALL); e != nil {
		return fmt.Errorf("%w: wait for the seized root: %w", ErrStart, e)
	}
	if !status.Stopped() {
		return startFailure(status)
	}
	if e := t.announce(root); errors.Is(e, unix.ENOSYS) || errors.Is(e, unix.EPERM) {
		// No pidfds here (an old kernel or a container's seccomp profile): run without a ledger.
		// Its reader then holds no root record and never proves a lost helper's tree gone.
		t.Ledger, t.noPidfds = nil, true
	} else if e != nil {
		return e
	}
	// End the group stop for good: only SIGCONT clears it, and a continue alone would leave it behind
	// for the command to inherit. The task stays in its stop until the continue.
	if e := unix.Kill(root, unix.SIGCONT); e != nil {
		return fmt.Errorf("%w: continue the root: %w", ErrStart, e)
	}
	if e := continueTask(root, 0); e != nil {
		return fmt.Errorf("%w: continue the root: %w", ErrStart, e)
	}
	return nil
}

// startFailure says why the root ended before its exec: the shim exits with exec's errno.
func startFailure(status unix.WaitStatus) error {
	if status.Exited() && status.ExitStatus() != shimFailed {
		return fmt.Errorf("%w: exec: %w", ErrStart, syscall.Errno(status.ExitStatus()))
	}
	return fmt.Errorf("%w: the root ended before its exec (status %#x)", ErrStart, uint32(status))
}

const traceOptions = unix.PTRACE_O_TRACEFORK | unix.PTRACE_O_TRACEVFORK | unix.PTRACE_O_TRACEVFORKDONE | unix.PTRACE_O_TRACECLONE | unix.PTRACE_O_TRACEEXEC | unix.PTRACE_O_TRACEEXIT | unix.PTRACE_O_TRACESECCOMP | unix.PTRACE_O_TRACESYSGOOD | unix.PTRACE_O_EXITKILL

// ptraceSeize is PTRACE_SEIZE with options: a seized tree's new tasks are held by a job-control trap,
// which SIGCONT does not clear.
func ptraceSeize(pid, options int) error {
	if _, _, e := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_SEIZE, uintptr(pid), 0, uintptr(options), 0, 0); e != 0 {
		return e
	}
	return nil
}

func ptraceListen(pid int) error {
	if _, _, e := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_LISTEN, uintptr(pid), 0, 0, 0, 0); e != 0 {
		return e
	}
	return nil
}

// refuseTask fails the creating syscall a task is stopped at. Tests replace it to kill the task first.
var refuseTask = failSyscall

// waitTask is the tracer's wait4. Tests replace it to hold a report back.
var waitTask = unix.Wait4

// continueTask resumes a task from a ptrace stop. Tests replace it to kill the task first.
var continueTask = unix.PtraceCont

// stepTask lets a task run to its next syscall stop. Tests replace it to kill the task first.
var stepTask = unix.PtraceSyscall

// createTask lets an admitted caller into its creating syscall, to report how that ended.
var createTask = unix.PtraceSyscall

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

// births keeps the tracer's TID-keyed accounting straight across report order. wait4 returns a
// creator's FORK/VFORK/CLONE event and the child's own reports in either order: the child's first
// stop, and even its exit status, may come first. Adding a child at its creator's event after its
// exit was collected would track a task that no longer exists. early counts such reports per TID,
// since one TID can be reported, reaped and reused before either event arrives.
//
// A tracee keeps its number until the tracer reaps it, except that a non-leader's exec frees it in
// de_thread, and the exec barrier lets no task of the tree be created between that and the EXEC event
// that drops the entry. A creator event naming a tracked TID therefore means an exec got past the
// barrier, and is reported as reborn.
type births struct {
	early   map[int]int  // reports that came before their creator's event
	pending map[int]bool // tracked from the creator's event; the child's own first report is due
}

func newBirths() *births {
	return &births{early: map[int]int{}, pending: map[int]bool{}}
}

// reported handles a report for tid (known: tracked) and says whether it is a new task's first report.
func (b *births) reported(tid int, known bool) bool {
	switch {
	case !known:
		b.early[tid]++
		return true
	case b.pending[tid]:
		delete(b.pending, tid)
	}
	return false
}

// created consumes a creation event for tid (known: tracked). track says the child is still to
// report itself and must be tracked as pending; reborn says tid names a task already tracked.
func (b *births) created(tid int, known bool) (track, reborn bool) {
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
	return true, known
}

// kept notes that the entry of tid now stands for a task that came past the barrier, whose creator's
// event may still be due. By then the task may have gone, so that event must not track it.
func (b *births) kept(tid int) {
	if !b.pending[tid] {
		b.early[tid]++
	}
}

// execed notes that an exec dropped the entry of a former TID.
func (b *births) execed(previous int) { delete(b.pending, previous) }

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
