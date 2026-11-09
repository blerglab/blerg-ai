//go:build linux

package shellruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"golang.org/x/sys/unix"
)

// These run real shells in real cgroups, so they need three things `go test` can't assume: a process
// outside a session's tree (clone3 and setuid are refused inside one), a writable cgroup, and a test
// process no tracer has run in, because a tracer leaves its filter on Go's threads. Run them alone:
//
//	BLERG_TEST_CGROUP_SHELLS=1 go test ./internal/shellruntime -run '^TestCgroup'
func requireCgroups(t *testing.T) string {
	t.Helper()
	if os.Getenv("BLERG_TEST_CGROUP_SHELLS") == "" {
		t.Skip("set BLERG_TEST_CGROUP_SHELLS=1 and run alone")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(status, []byte("NoNewPrivs:\t1")) {
		t.Skip("needs a process outside a session's tree")
	}
	dir, err := newShellCgroup()
	if err != nil {
		t.Skip("no writable cgroup v2:", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := emptyShellCgroup(ctx, dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

type cgroupShell struct {
	t    *testing.T
	p    *tracedProcess
	seen strings.Builder
}

func startCgroupShell(t *testing.T, dir string) *cgroupShell {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash:", err)
	}
	c, b, _, _ := fixture(t)
	identity, root := c.command.Identity, c.command.Identity.Cwd
	identity.Shell = bash
	p, err := startShell(identity, b.Root, root, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.stop(ctx)
	})
	s := &cgroupShell{t: t, p: p}
	s.send("PS1=ready-\\ \n")
	return s
}

func (s *cgroupShell) send(text string) {
	s.t.Helper()
	if err := s.p.input([]byte(text)); err != nil {
		s.t.Fatal(err)
	}
}

// expect waits for want and consumes the output up to it. Output is typed back, so callers build
// markers from arithmetic to see the result, not the echo.
func (s *cgroupShell) expect(want string) {
	s.t.Helper()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(s.seen.String(), want) {
		select {
		case chunk, ok := <-s.p.output:
			if !ok {
				s.t.Fatalf("shell ended waiting for %q: %s", want, s.seen.String())
			}
			s.seen.Write(chunk)
		case <-deadline:
			s.t.Fatalf("no %q in %s", want, s.seen.String())
		}
	}
	_, rest, _ := strings.Cut(s.seen.String(), want)
	s.seen.Reset()
	s.seen.WriteString(rest)
}

// number reads the digits after marker, which is on the screen as "marker=123".
func (s *cgroupShell) number(marker string) int {
	s.t.Helper()
	pattern := regexp.MustCompile(marker + `=(\d+)\r?\n`)
	deadline := time.After(20 * time.Second)
	for {
		if found := pattern.FindStringSubmatch(s.seen.String()); found != nil {
			value, err := strconv.Atoi(found[1])
			if err != nil {
				s.t.Fatal(err)
			}
			return value
		}
		select {
		case chunk, ok := <-s.p.output:
			if !ok {
				s.t.Fatalf("shell ended waiting for %q: %s", marker, s.seen.String())
			}
			s.seen.Write(chunk)
		case <-deadline:
			s.t.Fatalf("no %q in %s", marker, s.seen.String())
		}
	}
}

func alive(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	return !bytes.Contains(raw[bytes.LastIndexByte(raw, ')'):], []byte(") Z "))
}

func waitGone(t *testing.T, what string, gone func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !gone() {
		if time.Now().After(deadline) {
			t.Fatalf("%s still there", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func missing(dir string) func() bool {
	return func() bool { _, err := os.Stat(dir); return os.IsNotExist(err) }
}

// tasksIn counts the threads in dir and the cgroups beneath it.
func tasksIn(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.Name() == "cgroup.threads" {
			if raw, e := os.ReadFile(path); e == nil {
				n += len(strings.Fields(string(raw)))
			}
		}
		return nil
	})
	return n
}

// A same-user task needs no sudo to write its PID to the writable parent, and it may lose its parent too.
func TestCgroupFreezeRefusesWhileATaskThatLeftRuns(t *testing.T) {
	for name, launch := range map[string]string{
		"child":    "sh -c 'echo $$ > %[1]s/cgroup.procs; while :; do echo x >> %[2]s; sleep 0.01; done' &\n",
		"orphaned": "(setsid sh -c 'echo $$ > %[1]s/cgroup.procs; while :; do echo x >> %[2]s; sleep 0.01; done' &)\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := requireCgroups(t)
			s := startCgroupShell(t, dir)
			progress := filepath.Join(t.TempDir(), "progress")
			s.send(fmt.Sprintf(launch, filepath.Dir(dir), progress))
			size := func() int64 {
				info, err := os.Stat(progress)
				if err != nil {
					return 0
				}
				return info.Size()
			}
			waitGone(t, "the task to start", func() bool { return size() > 0 })
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := s.p.freeze(ctx)
			if !errors.Is(err, processcontainment.ErrUnproven) {
				t.Fatalf("freeze answered %v with a writer outside the cgroup", err)
			}
			if frozen, e := cgroupEvent(dir, "frozen"); e != nil || frozen {
				t.Fatalf("a refused freeze left the shell frozen: %v %v", frozen, e)
			}
			if err = s.p.stop(ctx); err != nil {
				t.Fatal(err)
			}
			before := size()
			time.Sleep(100 * time.Millisecond)
			if after := size(); after != before {
				t.Fatalf("the task outlived the shell: %d to %d", before, after)
			}
		})
	}
}

func TestCgroupFreezeStillProvesAShellWithSubgroups(t *testing.T) {
	dir := requireCgroups(t)
	s := startCgroupShell(t, dir)
	sub := filepath.Join(dir, "own")
	s.send(fmt.Sprintf("mkdir %[1]s && sh -c 'echo $$ > %[1]s/cgroup.procs; exec sleep 300' & echo child=$!\n", sub))
	s.number("child")
	waitGone(t, "the subgroup task", func() bool { return tasksIn(t, sub) > 0 })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.p.freeze(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.p.thaw(ctx); err != nil {
		t.Fatal(err)
	}
}

// BLG-326: past the limit a new task is refused, the shell lives, and the person reads why.
func TestCgroupRefusesPastTheTaskLimitAndRecovers(t *testing.T) {
	for name, bomb := range map[string]string{
		"descendant": "sh -c 'while sleep 300 & do :; done' & echo bomb=$!\n",
		"subgroup":   "mkdir %[1]s/own; sh -c 'echo $$ > %[1]s/own/cgroup.procs; while sleep 300 & do :; done' & echo bomb=$!\n",
		"classic":    "echo bomb=$((1+1)); b() { b | b & }; b\n",
		"shell":      "echo bomb=$((1+1)); for i in {1..3000}; do sleep 300 & done\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := requireCgroups(t)
			s := startCgroupShell(t, dir)
			if strings.Contains(bomb, "%") {
				bomb = fmt.Sprintf(bomb, dir)
			}
			s.send(bomb)
			s.number("bomb")
			peak := 0
			deadline := time.Now().Add(30 * time.Second)
			for !strings.Contains(s.seen.String(), "process limit reached") {
				if time.Now().After(deadline) {
					t.Fatalf("no limit line, %d tasks", tasksIn(t, dir))
				}
				if peak = max(peak, tasksIn(t, dir)); peak > 2*TasksPerShell {
					t.Fatalf("tree reached %d tasks", peak) // Cleanup ends it before it hurts the host.
				}
				select {
				case chunk := <-s.p.output:
					s.seen.Write(chunk)
				case <-time.After(5 * time.Millisecond):
				}
			}
			for range 100 {
				peak = max(peak, tasksIn(t, dir))
				time.Sleep(5 * time.Millisecond)
			}
			// The watcher reacts within its tick, so some overshoot is expected. It must stay a bound.
			if peak > 2*TasksPerShell {
				t.Fatalf("tree reached %d tasks", peak)
			}
			t.Logf("peak %d tasks", peak)
			// A renewal of the refusal must not thaw a turn end's freeze.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := s.p.freeze(ctx); err != nil {
				t.Fatal(err)
			}
			time.Sleep(3 * shellRefuseEvery)
			if frozen, err := cgroupEvent(dir, "frozen"); err != nil || !frozen {
				t.Fatalf("the limit undid a freeze: %v %v", frozen, err)
			}
			if err := s.p.thaw(ctx); err != nil {
				t.Fatal(err)
			}
			s.seen.Reset()
			s.send("\x03") // bash waits out a refused fork in the foreground.
			time.Sleep(200 * time.Millisecond)
			s.send("echo alive=$((40+2))\n")
			if got := s.number("alive"); got != 42 {
				t.Fatal(got)
			}
			// Clearing the tree gives the shell its processes back.
			raw, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
			if err != nil {
				t.Fatal(err)
			}
			shell := s.p.cgroup.root.Load()
			for field := range strings.FieldsSeq(string(raw)) {
				if pid, _ := strconv.Atoi(field); int64(pid) != shell {
					_ = unix.Kill(pid, unix.SIGKILL)
				}
			}
			_ = os.WriteFile(filepath.Join(dir, "own", "cgroup.kill"), []byte("1"), 0)
			waitGone(t, "the bomb", func() bool { return tasksIn(t, dir) < TasksPerShell/2 })
			// bash reports each ended job first, so the answer can take a while.
			s.seen.Reset()
			s.send("/bin/echo back=$((1+1)) || echo refused=1\n")
			if got := s.number("back"); got != 2 {
				t.Fatal(got)
			}
		})
	}
}

func TestTaskCeilingLeavesRoomForEveryoneElse(t *testing.T) {
	for _, c := range []struct {
		hard, tasks uint64
		limits      []uint64
		want        uint64
	}{
		{hard: 128000, tasks: 100, limits: []uint64{40000, 80000}, want: 30000},
		{hard: 8000, tasks: 100, limits: nil, want: 6000},
		{hard: unix.RLIM_INFINITY, tasks: 100, limits: nil, want: maxTaskCeiling},
		// Near the allowance already, the shell still gets a usable start and no more.
		{hard: 128000, tasks: 39000, limits: []uint64{40000}, want: 39000 + TasksPerShell},
		{hard: 2000, tasks: 1900, limits: nil, want: 2000},
	} {
		if got := taskCeiling(c.hard, c.tasks, c.limits); got != c.want {
			t.Fatalf("%+v: got %d", c, got)
		}
	}
}

func TestCgroupPidsLimits(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{filepath.Join(leaf, "pids.max"): "max\n", filepath.Join(root, "a", "pids.max"): "500\n", filepath.Join(root, "pids.max"): "900\n"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := pidsLimits(root, leaf); len(got) != 2 || got[0] != 500 || got[1] != 900 {
		t.Fatal(got)
	}
}

func TestCgroupShellStartsUnderItsCeiling(t *testing.T) {
	dir := requireCgroups(t)
	want := taskCeiling(rlimitNproc(t), userTasks(), machineTaskLimits())
	s := startCgroupShell(t, dir)
	s.send("bash -c 'echo soft=$(ulimit -Su); echo hard=$(ulimit -Hu)'\n")
	soft, hard := s.number("soft"), s.number("hard")
	if hard <= 0 || uint64(hard) > want+uint64(TasksPerShell) || soft > hard {
		t.Fatalf("soft %d hard %d, ceiling about %d", soft, hard, want)
	}
}

func rlimitNproc(t *testing.T) uint64 {
	t.Helper()
	var own unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &own); err != nil {
		t.Fatal(err)
	}
	return own.Max
}

// Killing tasks removes no cgroup, and a subgroup holds its parent up.
func TestCgroupStopRemovesNestedCgroups(t *testing.T) {
	dir := requireCgroups(t)
	s := startCgroupShell(t, dir)
	nested := filepath.Join(dir, "a", "b")
	s.send(fmt.Sprintf("mkdir -p %[1]s %[2]s/c; sh -c 'echo $$ > %[1]s/cgroup.procs; exec sleep 300' & echo child=$!\n", nested, dir))
	pid := s.number("child")
	waitGone(t, "the nested task", func() bool { return tasksIn(t, nested) > 0 })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.p.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if alive(pid) {
		t.Fatal("the nested task survived")
	}
	if !missing(dir)() {
		t.Fatal("cgroup left behind")
	}
}

func TestCgroupSweepRemovesNestedCgroups(t *testing.T) {
	requireCgroups(t)
	own, err := ownCgroup()
	if err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(own, shellCgroupPrefix+"2147483645.1")
	if err = os.MkdirAll(filepath.Join(dead, "shell", "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	sweepShellCgroups(own)
	if !missing(dead)() {
		t.Fatal("the dead daemon's directory is still there")
	}
}

func TestCgroupShellLeavesNoNewPrivsClear(t *testing.T) {
	s := startCgroupShell(t, requireCgroups(t))
	s.send("echo flag=$(sed -n 's/NoNewPrivs:[[:space:]]*//p' /proc/$$/status)\n")
	if got := s.number("flag"); got != 0 {
		t.Fatalf("no_new_privs is %d", got)
	}
}

// The ticket's bug. sudo at its password prompt is a setuid-root task the daemon could not signal.
func TestCgroupShellSudoIsSetuidRootAndStillHeld(t *testing.T) {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		t.Skip("sudo:", err)
	}
	info, err := os.Stat(sudo)
	if err != nil || info.Mode()&os.ModeSetuid == 0 {
		t.Skip("sudo is not setuid")
	}
	dir := requireCgroups(t)
	s := startCgroupShell(t, dir)
	s.send("sudo -k -p blerg$((1+1)): true\n") // The sum tells the prompt from the echoed command.
	s.expect("blerg2:")
	if strings.Contains(s.seen.String(), "no new privileges") {
		t.Fatal(s.seen.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		t.Fatal(err)
	}
	privileged := 0
	for field := range strings.FieldsSeq(string(raw)) {
		pid, _ := strconv.Atoi(field)
		var st unix.Stat_t
		if unix.Stat(fmt.Sprintf("/proc/%d", pid), &st) == nil && st.Uid == 0 {
			privileged = pid
		}
	}
	if privileged == 0 {
		t.Fatalf("no root task in the cgroup: %s", raw)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.p.freeze(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.p.thaw(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.p.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if alive(privileged) {
		t.Fatal("the setuid task survived")
	}
	if !missing(dir)() {
		t.Fatal("cgroup left behind")
	}
}

func TestCgroupFreezeStopsEveryTaskUnderForkLoad(t *testing.T) {
	dir := requireCgroups(t)
	s := startCgroupShell(t, dir)
	progress := filepath.Join(t.TempDir(), "progress")
	// dash vforks each command, and setsid leaves the shell's process group.
	s.send(fmt.Sprintf("setsid sh -c 'while :; do /bin/true; echo x >> %s; done' &\n", progress))
	s.send("for i in 1 2 3; do sh -c 'while :; do /bin/true; done' & done\n")
	size := func() int64 {
		info, err := os.Stat(progress)
		if err != nil {
			return 0
		}
		return info.Size()
	}
	waitGone(t, "no progress", func() bool { return size() > 0 })
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	for range 100 {
		call, stop := context.WithTimeout(ctx, 2*time.Second)
		if err := s.p.freeze(call); err != nil {
			stop()
			t.Fatal(err)
		}
		before := size()
		time.Sleep(5 * time.Millisecond)
		if after := size(); after != before {
			stop()
			t.Fatalf("grew from %d to %d while frozen", before, after)
		}
		if err := s.p.thaw(call); err != nil {
			stop()
			t.Fatal(err)
		}
		stop()
	}
	before := size()
	waitGone(t, "no progress after thaw", func() bool { return size() > before })
}

func TestCgroupShellStopEndsDetachedChildren(t *testing.T) {
	dir := requireCgroups(t)
	s := startCgroupShell(t, dir)
	s.send("setsid nohup sleep 300 >/dev/null 2>&1 & echo child=$!\n")
	pid := s.number("child")
	waitGone(t, "child to start", func() bool { return alive(pid) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.p.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if alive(pid) {
		t.Fatal("a detached child survived the stop")
	}
	if !missing(dir)() {
		t.Fatal("cgroup left behind")
	}
}

func TestCgroupShellExitEndsItsJobs(t *testing.T) {
	dir := requireCgroups(t)
	s := startCgroupShell(t, dir)
	s.send("setsid sleep 300 & echo child=$!\n")
	pid := s.number("child")
	s.send("exit\n")
	waitGone(t, "the job", func() bool { return !alive(pid) })
	waitGone(t, "the cgroup", missing(dir))
}

func TestCgroupStopIsIdempotentAndFreezeAfterEndAnswers(t *testing.T) {
	s := startCgroupShell(t, requireCgroups(t))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.p.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.p.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.p.freeze(ctx); !errors.Is(err, processcontainment.ErrUnproven) {
		t.Fatalf("missing freezer proof answered %v", err)
	}
}

// PTRACE_O_EXITKILL would have ended these with the helper. Here the daemon's side does.
func TestCgroupHelperCrashEndsTheShell(t *testing.T) {
	requireCgroups(t)
	t.Setenv("BLERG_HOST_SHELL_CGROUP", "1")
	c, b, g, key := fixture(t)
	g.Grant.Privileged = true
	g = ownershell.SignGrant(key, g.Grant)
	out, err := c.Open(t.Context(), b, g, c.key)
	if err != nil {
		t.Fatal(err)
	}
	c.sessionMu.Lock()
	var peer *helperPeer
	for _, candidate := range c.peers {
		peer = candidate
	}
	c.sessionMu.Unlock()
	if peer == nil || peer.shellCgroup == "" {
		t.Fatal("the shell did not get a cgroup")
	}
	if err = c.Input(t.Context(), g, 1, []byte("setsid sleep 300 & echo child=$!\n")); err != nil {
		t.Fatal(err)
	}
	s := &cgroupShell{t: t, p: &tracedProcess{output: make(chan []byte)}}
	go func() {
		for chunk := range out {
			s.p.output <- chunk
		}
		close(s.p.output)
	}()
	pid := s.number("child")
	if err = peer.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitGone(t, "the cgroup", missing(peer.shellCgroup))
	if alive(pid) {
		t.Fatal("a child survived its helper")
	}
}

// The persistent helper is what a native desktop runs, protected and nondumpable.
func TestCgroupPersistentHelperEndsCleanlyAndAfterACrash(t *testing.T) {
	requireCgroups(t)
	t.Setenv("BLERG_HOST_SHELL_CGROUP", "1")
	peer, g, _, dirty := persistentPeerFixture(t, 2*time.Second, func(command *Command) { command.Custody.Privileged = true })
	if peer.shellCgroup == "" {
		t.Fatal("the shell did not get a cgroup")
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "input", Grant: g, Seq: 1, Data: []byte("setsid sleep 300 &\n")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("lifetime did not end the helper")
	}
	peer.mu.Lock()
	clean := peer.clean
	peer.mu.Unlock()
	if !clean || dirty.Load() != 0 || !missing(peer.shellCgroup)() {
		t.Fatal("lifetime end was not a clean, empty cgroup")
	}

	uncertain := new(atomic.Int32)
	peer, g, _, _ = persistentPeerFixture(t, time.Minute, func(command *Command) {
		command.Custody.Privileged = true
		command.cleanupUncertain = func() { uncertain.Add(1) }
	})
	if err := peer.call(t.Context(), HelperFrame{Op: "input", Grant: g, Seq: 1, Data: []byte("setsid sleep 300 &\n")}); err != nil {
		t.Fatal(err)
	}
	waitGone(t, "the job to start", func() bool {
		raw, err := os.ReadFile(filepath.Join(peer.shellCgroup, "cgroup.procs"))
		return err == nil && len(strings.Fields(string(raw))) >= 2
	})
	if err := peer.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("crashed helper not joined")
	}
	peer.mu.Lock()
	clean = peer.clean
	peer.mu.Unlock()
	if !missing(peer.shellCgroup)() {
		t.Fatal("a crashed helper left its cgroup")
	}
	// The cgroup is empty, but a helper's crash stays no proof of a clean end: a task may have left it.
	if clean || uncertain.Load() != 1 {
		t.Fatal("helper crash was treated as physical cleanup proof")
	}
}

func TestCgroupSweepEndsOnlyADeadDaemonsShells(t *testing.T) {
	requireCgroups(t)
	own, err := ownCgroup()
	if err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(own, shellCgroupPrefix+"2147483646.1")
	if err = unix.Mkdir(dead, 0o755); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(dead, "shell")
	if err = unix.Mkdir(shell, 0o755); err != nil {
		t.Fatal(err)
	}
	live, err := newShellCgroup()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = emptyShellCgroup(ctx, live)
	})
	start := func(dir string) *exec.Cmd {
		tree := newCgroupTree(dir)
		attrs := &syscall.SysProcAttr{}
		if err := tree.place(attrs); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = unix.Close(tree.fd) }()
		cmd := exec.Command("sleep", "300")
		cmd.SysProcAttr = attrs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	stranded, kept := start(shell), start(live)
	sweepShellCgroups(own)
	if err = stranded.Wait(); err == nil {
		t.Fatal("the stranded shell was not killed")
	}
	if !missing(dead)() {
		t.Fatal("the dead daemon's directory is still there")
	}
	if !alive(kept.Process.Pid) {
		t.Fatal("a live daemon's shell was killed")
	}
	_ = kept.Process.Kill()
	_ = kept.Wait()
}

func TestCgroup2Mount(t *testing.T) {
	hybrid := "30 25 0:26 / /sys/fs/cgroup ro,nosuid - tmpfs tmpfs ro\n31 30 0:27 / /sys/fs/cgroup/unified rw,nosuid shared:9 - cgroup2 cgroup2 rw,nsdelegate\n"
	if got, err := cgroup2Mount(hybrid); err != nil || got != "/sys/fs/cgroup/unified" {
		t.Fatal(got, err)
	}
	for _, refused := range []string{
		"30 25 0:26 / /sys/fs/cgroup/cpu rw - cgroup cgroup rw,cpu\n",
		"31 30 0:27 /some/part /mnt/cg rw - cgroup2 cgroup2 rw\n",
		`31 30 0:27 / /mnt/a\040b rw - cgroup2 cgroup2 rw` + "\n",
	} {
		if got, err := cgroup2Mount(refused); err == nil {
			t.Fatalf("%q gave %q", refused, got)
		}
	}
}

func TestCgroupValidPath(t *testing.T) {
	if !validShellCgroup("/sys/fs/cgroup/a.service/blerg-shells-12.34/abcd") {
		t.Fatal("refused a real path")
	}
	for _, refused := range []string{"", "x", "/sys/fs/cgroup", "/sys/fs/cgroup/a.service", "/sys/fs/cgroup/blerg-shells-1.2/../../x"} {
		if validShellCgroup(refused) {
			t.Fatalf("accepted %q", refused)
		}
	}
}

func TestCgroupIsOptIn(t *testing.T) {
	t.Setenv("BLERG_HOST_SHELL_CGROUP", "")
	if hostShellCgroupEnabled() {
		t.Fatal("on by default")
	}
}

func TestCgroupPrivilegedSyncLeavesWritersRunning(t *testing.T) {
	requireCgroups(t)
	t.Setenv("BLERG_HOST_SHELL_CGROUP", "1")
	peer, g, _, _ := persistentPeerFixture(t, time.Minute, func(command *Command) { command.Custody.Privileged = true })
	if err := peer.call(t.Context(), HelperFrame{Op: "sync", SyncUntil: time.Now().Add(10 * time.Second).Unix()}); err != nil {
		t.Fatal(err)
	}
	frozen, err := cgroupEvent(peer.shellCgroup, "frozen")
	if err != nil || frozen {
		t.Fatalf("privileged sync froze processes: %v %v", frozen, err)
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "input", Grant: g, Seq: 1, Data: []byte("echo forbidden\n")}); err == nil {
		t.Fatal("sync allowed terminal input")
	}
	if err := peer.call(t.Context(), HelperFrame{Op: "thaw"}); err != nil {
		t.Fatal(err)
	}
}

func TestCgroupSwitchKeepsOrdinaryShellTraced(t *testing.T) {
	requireCgroups(t)
	t.Setenv("BLERG_HOST_SHELL_CGROUP", "1")
	c, b, g, _ := fixture(t)
	out, err := c.Open(t.Context(), b, g, c.key)
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range c.peers {
		if peer.shellCgroup != "" {
			t.Fatal("ordinary shell lost tracer")
		}
	}
	if err := c.Input(t.Context(), g, 1, []byte("printf '\\124\\122\\101\\103\\105\\104'\n")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "TRACED")
}

// Last: a tracer poisons this process for the tests above.
func TestCgroupTracerStillRefusesSudo(t *testing.T) {
	if _, err := exec.LookPath("sudo"); err != nil {
		t.Skip("sudo:", err)
	}
	requireCgroups(t)
	s := startCgroupShell(t, "")
	s.send("sudo -k -n true\n")
	s.expect("no new privileges")
}
