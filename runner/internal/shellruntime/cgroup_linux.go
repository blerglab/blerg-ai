//go:build linux

package shellruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"golang.org/x/sys/unix"
)

// A setuid exec needs a shell that is neither traced nor under no_new_privs, so a host shell that
// may use sudo is held by a cgroup instead (docs/design/host-shell-cgroup.md). The daemon makes the
// cgroup and removes it; the helper only starts the shell in it.
const shellCgroupPrefix = "blerg-shells-"

// hostShellCgroupEnabled offers the privileged mode; every shell still needs an explicit human choice.
func hostShellCgroupEnabled() bool { return os.Getenv("BLERG_HOST_SHELL_CGROUP") == "1" }

// RemoveEmptyShellCgroupParent removes only this daemon's empty parent. Live
// shells or subgroups make rmdir fail and remain available for recovery.
func RemoveEmptyShellCgroupParent() error {
	own, err := ownCgroup()
	if err != nil {
		return err
	}
	start, err := processStart(os.Getpid())
	if err != nil {
		return err
	}
	return removeEmptyShellCgroupParent(filepath.Join(own, fmt.Sprintf("%s%d.%s", shellCgroupPrefix, os.Getpid(), start)))
}

func removeEmptyShellCgroupParent(path string) error {
	err := unix.Rmdir(path)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

const (
	// Past the limit the root may start this many more, so a person can still run ps and kill.
	shellRootReserve = 64
	// Refusing ends this far under the limit, so a tree at the limit doesn't freeze on every tick.
	shellLimitSlack = 16
	// How often a shell's task count is read. It sets the overshoot; the ceiling is what holds.
	shellWatchEvery = 10 * time.Millisecond
	// How often a refusal is renewed for tasks made since, and how often the person is told.
	shellRefuseEvery = 100 * time.Millisecond
	shellNoticeEvery = time.Minute
	// A ceiling on a user's tasks past which a machine has no business going for one shell.
	maxTaskCeiling = 1 << 16
)

// cgroupRelative is the cgroup v2 line of a /proc/<pid>/cgroup file.
func cgroupRelative(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a kernel file
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(string(raw)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok && strings.HasPrefix(rest, "/") {
			return strings.TrimSuffix(rest, " (deleted)"), nil
		}
	}
	return "", errors.New("no cgroup v2 membership")
}

// ownCgroup is this process's cgroup v2 directory. Hybrid hosts (WSL) mount it beside the v1 controllers.
func ownCgroup() (string, error) {
	relative, err := cgroupRelative("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	mount, err := ownCgroup2Mount()
	if err != nil {
		return "", err
	}
	return filepath.Join(mount, relative), nil
}

func ownCgroup2Mount() (string, error) {
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	return cgroup2Mount(string(mounts))
}

// cgroup2Mount finds a mount of the whole hierarchy. A mount point with an escaped character is
// refused rather than decoded: no host has one, and the shell then keeps the tracer.
func cgroup2Mount(mountinfo string) (string, error) {
	for line := range strings.Lines(mountinfo) {
		before, after, ok := strings.Cut(line, " - ")
		fields := strings.Fields(before)
		if !ok || len(fields) < 5 || !strings.HasPrefix(after, "cgroup2 ") {
			continue
		}
		if fields[3] == "/" && !strings.Contains(fields[4], `\`) {
			return fields[4], nil
		}
	}
	return "", errors.New("no cgroup2 mount")
}

// processStart is the kernel's start time of pid, which tells a process from a later one with its number.
func processStart(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// comm can hold spaces and parentheses, so fields count from the last ')'.
	fields := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
	if len(fields) < 20 {
		return "", errors.New("short process status")
	}
	return fields[19], nil
}

func shellCgroupOwnerDead(pid int, start string, read func(int) (string, error)) (bool, error) {
	running, err := read(pid)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return running != start, nil
}

// newShellCgroup makes one shell's cgroup under a directory that names this daemon, so the next
// daemon can tell its dead predecessor's shells from a live daemon's. An error refuses privileged startup.
func newShellCgroup() (string, error) {
	own, err := ownCgroup()
	if err != nil {
		return "", err
	}
	start, err := processStart(os.Getpid())
	if err != nil {
		return "", err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return "", err
	}
	parent := filepath.Join(own, fmt.Sprintf("%s%d.%s", shellCgroupPrefix, os.Getpid(), start))
	if err = unix.Mkdir(parent, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
		return "", err
	}
	dir := filepath.Join(parent, hex.EncodeToString(nonce[:]))
	if err = unix.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	// cgroup.kill is Linux 5.14. Without it nothing could end a task this user can't signal.
	for _, name := range []string{"cgroup.kill", "cgroup.freeze", "cgroup.procs"} {
		if err = unix.Access(filepath.Join(dir, name), unix.W_OK); err != nil {
			_ = unix.Rmdir(dir)
			return "", err
		}
	}
	return dir, nil
}

// validShellCgroup checks a bootstrap value's shape. The bootstrap is trusted; this catches a daemon bug.
func validShellCgroup(dir string) bool {
	return filepath.IsAbs(dir) && filepath.Clean(dir) == dir && strings.HasPrefix(filepath.Base(filepath.Dir(dir)), shellCgroupPrefix)
}

func cgroupEvent(dir, name string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.events")) //nolint:gosec // a kernel file under the daemon's own cgroup
	if err != nil {
		return false, err
	}
	for line := range strings.Lines(string(raw)) {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), name+" "); ok {
			return value == "1", nil
		}
	}
	return false, fmt.Errorf("cgroup.events has no %s", name)
}

// removeCgroup removes dir and every cgroup under it, deepest first. They hold no tasks by now, but
// a subgroup holds its parent up.
func removeCgroup(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err = removeCgroup(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return unix.Rmdir(dir)
}

// emptyShellCgroup ends every task in dir and removes it once the kernel says it is empty. cgroup.kill
// and populated both cover subgroups. A task that left the cgroup is the caller's to end (see the
// design). A missing directory is already empty.
func emptyShellCgroup(ctx context.Context, dir string) error {
	for {
		populated, err := cgroupEvent(dir, "populated")
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !populated {
			if err = removeCgroup(dir); err == nil || errors.Is(err, os.ErrNotExist) {
				return nil
			}
			// A subgroup can read populated 0 a moment before it can go, and a task can still make one.
			if !errors.Is(err, unix.EBUSY) && !errors.Is(err, unix.ENOTEMPTY) {
				return err
			}
		} else if err = os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: cgroup not empty: %w", processcontainment.ErrUnproven, ctx.Err())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// SweepShellCgroups ends the shells of a daemon that is gone. Helpers die with their daemon, and
// their shells would not. Agent sessions and test daemons share this cgroup, so a directory whose
// daemon is alive is never touched.
func SweepShellCgroups() error {
	own, err := ownCgroup()
	if err != nil {
		return err
	}
	return sweepShellCgroups(own)
}

func sweepShellCgroups(own string) error {
	entries, err := os.ReadDir(own)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		name, ok := strings.CutPrefix(entry.Name(), shellCgroupPrefix)
		if !ok || !entry.IsDir() {
			continue
		}
		owner, start, ok := strings.Cut(name, ".")
		pid, err := strconv.Atoi(owner)
		if !ok || err != nil || pid <= 0 || start == "" {
			continue
		}
		dead, e := shellCgroupOwnerDead(pid, start, processStart)
		if e != nil {
			failures = append(failures, fmt.Errorf("observe shell cgroup owner %d: %w", pid, e))
		}
		if !dead {
			continue
		}
		parent := filepath.Join(own, entry.Name())
		shells, err := os.ReadDir(parent)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, shell := range shells {
			if shell.IsDir() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if e := emptyShellCgroup(ctx, filepath.Join(parent, shell.Name())); e != nil {
					failures = append(failures, e)
				}
				cancel()
			}
		}
		if e := unix.Rmdir(parent); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

// procTask is what /proc/<pid>/stat says of a process.
type procTask struct {
	pid, ppid, threads int
	dead               bool // a zombie with no thread left, which runs nothing
	start              string
}

func readProcTask(pid int) (procTask, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procTask{}, err
	}
	// comm can hold spaces and parentheses, so fields count from the last ')'.
	fields := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
	if len(fields) < 20 {
		return procTask{}, errors.New("short process status")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procTask{}, err
	}
	threads, err := strconv.Atoi(fields[17])
	if err != nil {
		return procTask{}, err
	}
	return procTask{pid: pid, ppid: ppid, threads: threads, dead: fields[0] == "Z" && threads <= 1, start: fields[19]}, nil
}

func procTasks() ([]procTask, error) {
	return procTasksFrom("/proc", readProcTask)
}

func procTasksFrom(dir string, read func(int) (procTask, error)) ([]procTask, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	tasks := make([]procTask, 0, len(entries))
	for _, entry := range entries {
		pid, e := strconv.Atoi(entry.Name())
		if e != nil {
			continue
		}
		// Only disappearance proves an exit; permission and parse errors invalidate the proof.
		if task, e := read(pid); e == nil {
			tasks = append(tasks, task)
		} else if !errors.Is(e, os.ErrNotExist) && !errors.Is(e, unix.ESRCH) {
			return nil, e
		}
	}
	return tasks, nil
}

// descendants lists every process under root. Whatever a shell starts stays under its helper: an
// orphan comes to the helper, the subreaper. Only a service manager's own child, as `systemd-run
// --user` makes, is outside this tree, and the tracer can't follow that either.
func descendants(root int) ([]procTask, error) {
	tasks, err := procTasks()
	if err != nil {
		return nil, err
	}
	children := make(map[int][]procTask, len(tasks))
	for _, task := range tasks {
		children[task.ppid] = append(children[task.ppid], task)
	}
	var found []procTask
	for queue := children[root]; len(queue) > 0; queue = queue[1:] {
		found = append(found, queue[0])
		queue = append(queue, children[queue[0].pid]...)
	}
	return found, nil
}

// taskCeiling is the most tasks a shell's user may run before the shell is refused. RLIMIT_NPROC
// counts the whole user, so this can't be a shell's share. It keeps a quarter of what the machine
// allows for everyone else, and leaves a shell started on a busy machine a limit's worth.
func taskCeiling(hard, tasks uint64, limits []uint64) uint64 {
	lowest := hard
	for _, limit := range limits {
		lowest = min(lowest, limit)
	}
	ceiling := min(lowest/4*3, maxTaskCeiling)
	if tasks+TasksPerShell > ceiling {
		ceiling = tasks + TasksPerShell
	}
	return min(ceiling, hard)
}

// pidsLimits reads pids.max from leaf up to root, closest first.
func pidsLimits(root, leaf string) []uint64 {
	var limits []uint64
	for dir := leaf; ; dir = filepath.Dir(dir) {
		raw, err := os.ReadFile(filepath.Join(dir, "pids.max")) //nolint:gosec // a kernel file
		if err == nil {
			if limit, e := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); e == nil {
				limits = append(limits, limit)
			}
		}
		if dir == root || dir == filepath.Dir(dir) {
			return limits
		}
	}
}

// machineTaskLimits gathers what bounds this user's tasks: threads-max, and pids.max above this
// process on either hierarchy, since a hybrid host keeps pids on v1.
func machineTaskLimits() []uint64 {
	var limits []uint64
	if raw, err := os.ReadFile("/proc/sys/kernel/threads-max"); err == nil {
		if limit, e := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); e == nil {
			limits = append(limits, limit)
		}
	}
	if own, err := ownCgroup(); err == nil {
		if mount, e := ownCgroup2Mount(); e == nil {
			limits = append(limits, pidsLimits(mount, own)...)
		}
	}
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return limits
	}
	for line := range strings.Lines(string(raw)) {
		if _, relative, ok := strings.Cut(strings.TrimSpace(line), ":pids:"); ok {
			limits = append(limits, pidsLimits("/sys/fs/cgroup/pids", filepath.Join("/sys/fs/cgroup/pids", relative))...)
		}
	}
	return limits
}

// userTasks counts the threads of the processes this user owns.
func userTasks() uint64 {
	tasks, err := procTasks()
	if err != nil {
		return 0
	}
	uid, total := uint32(os.Getuid()), uint64(0) //nolint:gosec // Linux getuid is a nonnegative uint32.
	for _, task := range tasks {
		var st unix.Stat_t
		if unix.Stat(fmt.Sprintf("/proc/%d", task.pid), &st) == nil && st.Uid == uid {
			total += uint64(task.threads) //nolint:gosec // the kernel's count
		}
	}
	return total
}

// cgroupFiles lists one kernel file of dir and of each cgroup beneath it.
func cgroupFiles(dir, name string) []string {
	files := []string{filepath.Join(dir, name)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}
	for _, entry := range entries {
		if entry.IsDir() {
			files = append(files, cgroupFiles(filepath.Join(dir, entry.Name()), name)...)
		}
	}
	return files
}

func cgroupThreads(dir string) int {
	count := 0
	for _, path := range cgroupFiles(dir, "cgroup.threads") {
		if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // a kernel file
			count += bytes.Count(raw, []byte("\n"))
		}
	}
	return count
}

// cgroupTree answers as processcontainment.Tree does for a shell held by a cgroup.
type cgroupTree struct {
	dir      string
	fd       int
	kill     chan struct{}
	killOnce sync.Once
	done     chan struct{}
	err      error
	mu       sync.Mutex
	root     atomic.Int64
	mount    string
	// reaped counts what the helper collected, so a process changing under a /proc scan shows.
	reaped atomic.Uint64
	// held is a freeze of Control's, which the limit must not undo.
	held       bool
	atLimit    chan struct{}
	soft, hard uint64
}

func newCgroupTree(dir string) *cgroupTree {
	return &cgroupTree{dir: dir, fd: -1, kill: make(chan struct{}), done: make(chan struct{}), atLimit: make(chan struct{}, 1)}
}

// place makes the shell start inside the cgroup, so it runs no instruction outside it.
func (t *cgroupTree) place(attrs *syscall.SysProcAttr) error {
	fd, err := unix.Open(t.dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	t.fd = fd
	attrs.UseCgroupFD = true
	attrs.CgroupFD = fd
	return nil
}

// AtLimit signals that the shell's tree reached its limit, at most once in shellNoticeEvery.
func (t *cgroupTree) AtLimit() <-chan struct{} { return t.atLimit }

// outside lists the live tasks that left the cgroup. Taken with the cgroup frozen and no reap during
// it, what it finds is complete; a reap means something ran, and the scan is taken again.
func (t *cgroupTree) outside() (tasks []procTask, settled bool, err error) {
	reaped := t.reaped.Load()
	all, err := descendants(os.Getpid())
	if err != nil {
		return nil, false, err
	}
	for _, task := range all {
		if task.dead {
			continue
		}
		relative, e := cgroupRelative(fmt.Sprintf("/proc/%d/cgroup", task.pid))
		if errors.Is(e, os.ErrNotExist) || errors.Is(e, unix.ESRCH) {
			continue
		}
		if e != nil {
			return nil, false, e
		}
		if path := filepath.Join(t.mount, relative); path != t.dir && !strings.HasPrefix(path, t.dir+"/") {
			tasks = append(tasks, task)
		}
	}
	return tasks, t.reaped.Load() == reaped, nil
}

// endOutside ends what left the cgroup, once the cgroup is gone. A pidfd holds the process it was
// checked to be. A root task can't be signaled, and is reported.
func (t *cgroupTree) endOutside(ctx context.Context) error {
	for {
		tasks, settled, err := t.outside()
		if err != nil {
			return err
		}
		if len(tasks) == 0 && settled {
			return nil
		}
		for _, task := range tasks {
			fd, e := unix.PidfdOpen(task.pid, 0)
			if e != nil {
				continue
			}
			if current, e := readProcTask(task.pid); e == nil && current.start == task.start {
				e = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
				if errors.Is(e, unix.EPERM) {
					_ = unix.Close(fd)
					return fmt.Errorf("process %d left the cgroup and can't be ended: %w", task.pid, e)
				}
			}
			_ = unix.Close(fd)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("a task that left the cgroup is still running: %w", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

// bound gives a shell its ceiling and starts counting. Only the kernel can refuse a process
// without the tracer, and only RLIMIT_NPROC reaches a host without a delegated pids controller.
func (t *cgroupTree) bound() error {
	var own unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &own); err != nil {
		return err
	}
	t.hard = taskCeiling(own.Max, userTasks(), machineTaskLimits())
	t.soft = min(own.Cur, t.hard)
	// Run owns a dedicated helper process. Set its inherited limit before the fork;
	// lowering the child's limit after exec lets its first command fork without it.
	return unix.Setrlimit(unix.RLIMIT_NPROC, &unix.Rlimit{Cur: t.soft, Max: t.hard})
}

// watch holds the tree to maximum, as the tracer's refusal does. Lowering a task's soft limit under
// the user's count makes its next fork fail with EAGAIN, and the shell goes on. The ceiling stays
// under it all, so a task raising its own soft limit gets no further than that, and neither does
// one that left the cgroup. Root tasks don't count against RLIMIT_NPROC and can't be reached.
func (t *cgroupTree) watch(maximum int, stop <-chan struct{}) {
	ticker := time.NewTicker(shellWatchEvery)
	defer ticker.Stop()
	refusing := 0 // 0 nothing, 1 all but the root, 2 the root too
	var renewed, told time.Time
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		count := cgroupThreads(t.dir)
		want := 0
		switch {
		case count >= maximum+shellRootReserve:
			want = 2
		case count >= maximum:
			want = 1
		case refusing > 0 && count >= maximum-shellLimitSlack:
			want = 1
		}
		if want == refusing && (want == 0 || time.Since(renewed) < shellRefuseEvery) {
			continue
		}
		t.refuse(want)
		refusing, renewed = want, time.Now()
		if want > 0 && time.Since(told) >= shellNoticeEvery {
			told = renewed
			select {
			case t.atLimit <- struct{}{}:
			default:
			}
		}
	}
}

// refuse sets every task's soft limit with the tree held still, so none is missed for a fork in
// flight. Anything the kernel refuses here is a task this user can't change, which it doesn't limit.
func (t *cgroupTree) refuse(level int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	path := filepath.Join(t.dir, "cgroup.freeze")
	if !t.held {
		_ = os.WriteFile(path, []byte("1"), 0)
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if frozen, err := cgroupEvent(t.dir, "frozen"); err != nil || frozen {
				break
			}
		}
		defer func() { _ = os.WriteFile(path, []byte("0"), 0) }()
	}
	root := int(t.root.Load())
	for _, file := range cgroupFiles(t.dir, "cgroup.procs") {
		raw, err := os.ReadFile(file) //nolint:gosec // a kernel file
		if err != nil {
			continue
		}
		for field := range strings.FieldsSeq(string(raw)) {
			pid, err := strconv.Atoi(field)
			if err != nil {
				continue
			}
			soft := t.soft
			if level > 1 || (level == 1 && pid != root) {
				soft = 1
			}
			_ = unix.Prlimit(pid, unix.RLIMIT_NPROC, &unix.Rlimit{Cur: soft, Max: t.hard}, nil)
		}
	}
}

// Run must have the helper's child-reaper role to itself, as Tree.Run does. The root's exit ends the
// rest of the cgroup, and Run returns only when the root is reaped and the cgroup is gone.
func (t *cgroupTree) Run(cmd *exec.Cmd, start func() error, maximum int, ready chan<- error) (code int, err error) {
	runtime.LockOSThread() // Pdeathsig belongs to the starting thread.
	defer runtime.UnlockOSThread()
	defer func() {
		if t.fd >= 0 {
			_ = unix.Close(t.fd)
		}
		t.err = err
		close(t.done)
	}()
	if e := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); e != nil {
		e = fmt.Errorf("%w: set child subreaper: %w", processcontainment.ErrUnproven, e)
		ready <- e
		return 1, e
	}
	mount, e := ownCgroup2Mount()
	if e != nil {
		e = fmt.Errorf("%w: find cgroup mount: %w", processcontainment.ErrUnproven, e)
		ready <- e
		return 1, e
	}
	t.mount = mount
	if e := t.bound(); e != nil {
		e = fmt.Errorf("%w: set inherited task ceiling: %w", processcontainment.ErrUnproven, e)
		ready <- e
		return 1, e
	}
	if e := start(); e != nil {
		ready <- e
		return 1, e
	}
	defer func() { _ = cmd.Process.Release() }()
	root := cmd.Process.Pid
	t.root.Store(int64(root))
	ended := make(chan int, 1)
	go t.reapShell(root, ended)
	stopWatch, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		t.watch(maximum, stopWatch)
	}()
	ready <- nil
	stopped := false
	select {
	case code = <-ended:
	case <-t.kill:
		stopped = true
	}
	close(stopWatch)
	<-watched
	if stopped {
		// A root that moved itself out is not reached by cgroup.kill, and a root shell is not reached
		// by this: each covers the other.
		_ = cmd.Process.Kill()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e := emptyShellCgroup(ctx, t.dir); e != nil {
		return 1, fmt.Errorf("%w: %w", processcontainment.ErrUnproven, e)
	}
	if e := t.endOutside(ctx); e != nil {
		return 1, fmt.Errorf("%w: %w", processcontainment.ErrUnproven, e)
	}
	if stopped {
		code = <-ended
	}
	return code, nil
}

// reapShell collects the root and every orphan until none is left. It stays with the helper past
// the root, for orphans of a shell that ended.
func (t *cgroupTree) reapShell(root int, ended chan<- int) {
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WALL, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return
		}
		t.reaped.Add(1)
		if pid == root {
			if status.Exited() {
				ended <- status.ExitStatus()
			} else {
				ended <- 128 + int(status.Signal())
			}
		}
	}
}

// Control freezes or thaws, and acknowledges only what cgroup.events reports. frozen speaks for the
// cgroup, so a freeze also needs every task of the shell to be in it. A freeze that can't finish is
// undone, so it costs that turn's copy, not the shell.
func (t *cgroupTree) Control(ctx context.Context, op string) error {
	want, value := true, "1"
	switch op {
	case "freeze":
	case "thaw":
		want, value = false, "0"
	default:
		return fmt.Errorf("%w: unknown control %s", processcontainment.ErrUnproven, op)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	path := filepath.Join(t.dir, "cgroup.freeze")
	if !want {
		t.held = false
	}
	if err := os.WriteFile(path, []byte(value), 0); err != nil {
		return fmt.Errorf("%w: write %s: %w", processcontainment.ErrUnproven, op, err)
	}
	for {
		frozen, err := cgroupEvent(t.dir, "frozen")
		if err != nil {
			return fmt.Errorf("%w: read %s acknowledgement: %w", processcontainment.ErrUnproven, op, err)
		}
		if frozen == want {
			if !want {
				return nil
			}
			left, settled, err := t.outside()
			if err == nil && len(left) == 0 && settled {
				t.held = true
				return nil
			}
			if err != nil || len(left) > 0 {
				_ = os.WriteFile(path, []byte("0"), 0)
				if err != nil {
					return fmt.Errorf("%w: reading the shell's tasks: %w", processcontainment.ErrUnproven, err)
				}
				return fmt.Errorf("%w: %d tasks left the cgroup", processcontainment.ErrUnproven, len(left))
			}
		}
		select {
		case <-t.done:
			return t.err
		case <-ctx.Done():
			if op == "freeze" {
				_ = os.WriteFile(path, []byte("0"), 0)
			}
			return fmt.Errorf("%w: waiting for %s acknowledgement: %w", processcontainment.ErrUnproven, op, ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

func (t *cgroupTree) Stop(ctx context.Context) error {
	t.killOnce.Do(func() { close(t.kill) })
	select {
	case <-t.done:
		return t.err
	case <-ctx.Done():
		return fmt.Errorf("%w: waiting for cgroup to empty: %w", processcontainment.ErrUnproven, ctx.Err())
	}
}
