// Package hostgit is the one way the daemon, the server and a session pod run git themselves (not
// the agent's own git). A repository's configuration is written by whoever works in it, an agent
// included, so nothing it says may run a command, reach the network, read a file it names, recurse
// into a submodule or rewrite history as git shows it. Every call gets the same hardening; a call
// site adds only what it legitimately needs (Options), and each such difference is named there.
//
// What every call gets:
//   - its own environment, never the daemon's: no inherited GIT_* variable, no secret, no system
//     or global configuration (HOME=/nonexistent), no prompt, LC_ALL=C, no replace objects, no
//     grafts (GIT_GRAFT_FILE=/dev/null), no optional locks;
//   - transports: GIT_ALLOW_PROTOCOL names the allowed ones ("none" by default), which no
//     repository configuration can outrank, and every protocol.<name>.allow is pinned as well;
//     no partial-clone lazy fetch unless asked for;
//   - -c overrides of every key that runs a command (hooks, fsmonitor, the ssh command, an
//     external diff, a submodule's diff, the credential helper, askpass), recurses into submodules,
//     reads a file it names (the diff order file, the mailmap, the attributes file) or checks a
//     signature;
//   - its own process group, killed whole before reaping on timeout, cancel or completion, so no
//     helper outlives the call;
//   - with MemoryLimit, an address-space limit on Linux or a process-group RSS supervisor on
//     macOS, so a reader something still makes read without end fails fast.
package hostgit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// MemoryLimitKB is the default memory budget under Options.MemoryLimit: address space on Linux,
// observed process-group RSS on macOS. Pack windows are kept well under it (Config).
const MemoryLimitKB = 1 << 20

// limitScript runs git under `ulimit -v`. It is a constant: the limit, git's path and its
// arguments are positional parameters, never parsed by the shell.
const limitScript = `ulimit -v "$1" || exit 125; shift; exec "$@"`

// Protocols git knows a transport for; each is allowed or pinned to never.
var protocols = []string{"ext", "fd", "file", "git", "http", "https", "ssh", "testgit"}

// Options are what a call site needs beyond the hardening every call gets.
type Options struct {
	// Diagnostic is an optional private timing sink. It receives no argv or environment.
	// It may be called by the RSS goroutine; callers must support concurrent calls.
	Diagnostic func(stage string, elapsed time.Duration, err error)
	// Dir is git's working directory. With ExtraFiles it may name an inherited descriptor
	// (/proc/self/fd/N): a pinned directory.
	Dir string
	// DirFile pins cwd on Darwin, where chdir through /dev/fd/N is unsupported.
	// Run owns its thread-local launch; Linux continues to use Dir.
	DirFile    *os.File
	ExtraFiles []*os.File
	// SafeDirectory is the one directory safe.directory names ("" for none).
	SafeDirectory string
	// Ceiling sets GIT_CEILING_DIRECTORIES: git never looks for a repository above it.
	Ceiling string
	// Protocols are the transports this call may use (file for a local clone or fetch, https for
	// a remote clone); none by default.
	Protocols []string
	// LazyFetch allows a partial clone to fetch a missing object (a session pod's blobless clone).
	LazyFetch bool
	// UserConfig keeps the person's own configuration and environment (HOME, the system and
	// global git configuration, PATH, proxies, the ssh agent): a clone from a remote into a new
	// folder, which has no repository configuration of its own yet and may need the person's
	// credential helper or ssh keys. Inherited GIT_* variables are still dropped, and a secret
	// is never passed (Env holds the daemon's own sanitized environment for this).
	UserConfig bool
	// UserEnv is the environment UserConfig starts from (the daemon's sanitized environment; nil:
	// this process's). GIT_* variables in it are dropped.
	UserEnv []string
	// SystemConfig keeps the system git configuration (a session pod: its image's /etc/gitconfig,
	// CA settings), still without the global one.
	SystemConfig bool
	// PassEnv names variables copied from this process's environment (proxies, CA files).
	PassEnv []string
	// Env is added to the environment (a token header for one host, low-speed limits).
	Env []string
	// Config is added to the -c overrides (key=value), after the shared ones.
	Config []string
	// Untrusted: the repository's object store and metadata are someone else's (a session's
	// workspace or sandbox clone). Its optional binary indexes are not parsed: no commit-graph,
	// multi-pack index or bitmaps, no sparse walk, no filter served (UntrustedConfig).
	Untrusted bool
	// MemoryLimit bounds address space on Linux and observed process-group RSS on macOS:
	// MemoryLimitKB, or LimitKB when set, with small pack windows. On macOS use Run/Output,
	// which own the resident-memory supervisor and join it before returning.
	MemoryLimit bool
	LimitKB     int
	// Timeout bounds the call (0: only ctx).
	Timeout time.Duration
	// Input is bounded, in-memory stdin; arbitrary blocking readers are not accepted.
	Input []byte
	// Stdout and Stderr receive git's output (nil: discarded). Custom writers must
	// implement ContextWriter; known nonblocking buffers and *os.File are also accepted.
	Stdout, Stderr io.Writer
}

// Config is every -c override a call carries, before Options.Config.
func Config(o Options) []string {
	kv := []string{
		"core.hooksPath=/dev/null", "core.fsmonitor=false",
		"diff.external=", "diff.submodule=short", "submodule.recurse=false", "fetch.recurseSubmodules=false",
		"status.submoduleSummary=false", "log.showSignature=false",
		// No background work a call would leave behind (gc --auto detaches out of the process
		// group), and no hint that hides the real error (GIT_GRAFT_FILE makes git 2.34 print one).
		"gc.auto=0", "maintenance.auto=false", "fetch.writeCommitGraph=false", "advice.graftFileDeprecated=false",
		// A fetch, a push and both serving sides list an alternate's refs with this command:
		// a fixed one that lists none.
		"core.alternateRefsCommand=true",
	}
	if o.Untrusted {
		kv = append(kv, UntrustedConfig...)
	}
	if !o.UserConfig {
		// The person's own credential helper, mailmap or attributes file is theirs to keep in a
		// clone from a remote; anywhere else these name a command or a file the repository chose.
		kv = append(kv, "credential.helper=", "core.askPass=",
			"diff.orderFile=/dev/null", "log.mailmap=false", "mailmap.file=/dev/null", "mailmap.blob=",
			"core.attributesFile=/dev/null")
	}
	allowed := map[string]bool{}
	for _, p := range o.Protocols {
		allowed[p] = true
	}
	switch {
	case !allowed["ssh"]:
		kv = append(kv, "core.sshCommand=false")
	case !o.UserConfig:
		kv = append(kv, "core.sshCommand=ssh") // never one the repository names
	}
	if len(o.Protocols) == 0 {
		kv = append(kv, "protocol.allow=never")
	}
	for _, p := range protocols {
		if allowed[p] {
			kv = append(kv, "protocol."+p+".allow=always")
		} else {
			kv = append(kv, "protocol."+p+".allow=never")
		}
	}
	if o.SafeDirectory != "" {
		kv = append(kv, "safe.directory="+o.SafeDirectory)
	}
	if o.MemoryLimit {
		kv = append(kv, "core.packedGitWindowSize=32m", "core.packedGitLimit=256m", "core.deltaBaseCacheLimit=64m")
	}
	kv = append(kv, o.Config...)
	out := make([]string, 0, 2*len(kv)+1)
	out = append(out, "--no-pager")
	for _, v := range kv {
		out = append(out, "-c", v)
	}
	return out
}

// UntrustedConfig are the overrides for a repository whose object store someone else writes, on
// both sides of a local fetch (UploadPackCommand carries them too): its optional binary indexes are
// not read.
var UntrustedConfig = []string{
	"core.commitGraph=false", "core.multiPackIndex=false", "pack.useBitmaps=false", "pack.writeBitmapHashCache=false",
	"pack.useSparse=false", "uploadpack.allowFilter=false",
}

// withoutLazyFetchVar leaves GIT_NO_LAZY_FETCH out (tests only): the protocol block must hold on
// its own, on a git that does not know the variable.
var withoutLazyFetchVar = false

// SetWithoutLazyFetchVarForTest is for tests in other packages; it returns the restore.
func SetWithoutLazyFetchVarForTest(v bool) func() {
	prev := withoutLazyFetchVar
	withoutLazyFetchVar = v
	return func() { withoutLazyFetchVar = prev }
}

// defaultPath is where git and its helpers are looked for, after git's own directory.
const defaultPath = "/usr/local/bin:/usr/bin:/bin"

// Environ is the environment of a call (git is the resolved binary).
func Environ(o Options, git string) []string {
	var env []string
	if o.UserConfig {
		base := o.UserEnv
		if base == nil {
			base = os.Environ()
		}
		for _, e := range base {
			key, _, _ := strings.Cut(e, "=")
			if strings.HasPrefix(key, "GIT_") || key == "LC_ALL" {
				continue
			}
			env = append(env, e)
		}
	} else {
		env = []string{"PATH=" + filepath.Dir(git) + ":" + defaultPath, "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=/dev/null"}
		if !o.SystemConfig {
			env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_ATTR_NOSYSTEM=1")
		}
		for _, name := range o.PassEnv {
			if v, ok := os.LookupEnv(name); ok && !strings.HasPrefix(name, "GIT_") {
				env = append(env, name+"="+v)
			}
		}
	}
	allow := "none"
	if len(o.Protocols) > 0 {
		allow = strings.Join(o.Protocols, ":")
	}
	env = append(env, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=/dev/null",
		"GIT_ALLOW_PROTOCOL="+allow, "GIT_PROTOCOL_FROM_USER=0")
	if !o.LazyFetch && !withoutLazyFetchVar {
		env = append(env, "GIT_NO_LAZY_FETCH=1")
	}
	if o.Ceiling != "" {
		env = append(env, "GIT_CEILING_DIRECTORIES="+o.Ceiling)
	}
	return append(env, o.Env...)
}

// ErrNoGit: Git is absent or Darwin's developer executable cannot be trusted.
var ErrNoGit = errors.New("git is not available on this machine")

var testBinary atomic.Pointer[string]

// SetBinaryForTest installs a process-local fixture executable. There is no
// environment/configuration override; production must never call this seam.
// Tests must serialize changes and restore only after joining their Git calls.
func SetBinaryForTest(path string) func() {
	previous := testBinary.Swap(&path)
	return func() { testBinary.Store(previous) }
}

// Initialize resolves Darwin Git before request budgets begin. It is a no-op
// elsewhere, preserving Linux's per-call PATH lookup.
func Initialize() error { return platformInitializeGit() }

// Binary uses cached, validated developer Git on Darwin; elsewhere it retains
// per-call PATH lookup so installed Git can change while the daemon runs.
func Binary() (string, error) {
	if path := testBinary.Load(); path != nil {
		return *path, nil
	}
	return platformGitBinary()
}

// Command is the hardened git command for args, not started: for a caller that runs it itself
// (it must kill the group before reaping and must never Wait concurrently with KillGroup).
// Prefer Run, which owns that ordering. Bounded Darwin commands require Run/Output;
// a raw exec.Cmd cannot own the memory supervisor or descriptor-cwd launch, so Command refuses
// either combination on Darwin.
func Command(o Options, args ...string) (*exec.Cmd, error) {
	if runtime.GOOS == "darwin" && (o.MemoryLimit || o.DirFile != nil) {
		return nil, &memoryRefusal{fmt.Errorf("%w: supervised Darwin commands require Run or Output", ErrMemoryMonitor)}
	}
	return commandForPlatform(o, runtime.GOOS, args...)
}
func commandForPlatform(o Options, platform string, args ...string) (*exec.Cmd, error) {
	git, err := Binary()
	if err != nil {
		return nil, err
	}
	argv := append(Config(o), args...)
	name := git
	if o.MemoryLimit && platform == "linux" {
		limit := memoryLimitKB(o)
		argv = append([]string{"-c", limitScript, "blerg-git", strconv.Itoa(limit), git}, argv...)
		name = "/bin/sh"
	}
	cmd := exec.Command(name, argv...) //nolint:gosec,noctx // G204: the git binary (or a constant sh script exec'ing it) with argv built by the call site; no shell parses any value. noctx: Run bounds it and kills the process group itself.
	cmd.Dir = o.Dir
	if platform == "darwin" && o.DirFile != nil {
		cmd.Dir = "" // platformStartCommand sets cwd from the held directory on a private thread.
	}
	cmd.ExtraFiles = o.ExtraFiles
	cmd.Env = Environ(o, git)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if o.Input != nil {
		cmd.Stdin = bytes.NewReader(o.Input)
	}
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	return cmd, nil
}

// KillGroup kills a started, unreaped command's process group. The caller must
// exclusively own reaping: never call concurrently with Wait. Run owns this ordering.
// Completed commands are ignored; no numeric group signal is safe after reap.
func KillGroup(cmd *exec.Cmd) {
	killGroup(cmd, syscall.Kill)
}

func killGroup(cmd *exec.Cmd, kill func(int, syscall.Signal) error) {
	if cmd != nil && cmd.Process != nil && cmd.ProcessState == nil {
		_ = kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Run runs git with args under o, bounded by ctx and o.Timeout; the whole process group is killed
// on either, and once git is done.
func Run(ctx context.Context, o Options, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := run(ctx.Done(), o, args...)
	if errors.Is(err, errStopped) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return context.DeadlineExceeded
	}
	return err
}

var errStopped = errors.New("git was stopped")

// run is Run with stop in place of a context (nil: only o.Timeout bounds it).
func run(stop <-chan struct{}, o Options, args ...string) error {
	var timeout <-chan time.Time
	if o.Timeout > 0 {
		timer := time.NewTimer(o.Timeout)
		defer timer.Stop()
		timeout = timer.C
	}
	preparing := time.Now()
	recordDiagnostic(o, "command begin", preparing, nil)
	cmd, err := commandForPlatform(o, runtime.GOOS, args...)
	recordDiagnostic(o, "command prepared", preparing, err)
	if err != nil {
		return err
	}
	return runCommand(stop, timeout, o, cmd, platformMemoryWatch)
}

func runCommand(stop <-chan struct{}, timeout <-chan time.Time, o Options, cmd *exec.Cmd, watch func(Options, *exec.Cmd) *memoryWatch) error {
	output := newCommandOutput()
	defer output.cancel()
	if err := output.prepare(cmd); err != nil {
		return err
	}
	started := time.Now()
	recordDiagnostic(o, "start begin", started, nil)
	err := platformStartCommand(o, cmd)
	recordDiagnostic(o, "start", started, err)
	if err != nil {
		return err
	}
	// No Wait may run until cleanup has stopped using the numeric process group.
	exited := make(chan error, 1)
	go func() { exited <- platformObserveExit(cmd.Process.Pid) }()
	waiting := time.Now()
	recordDiagnostic(o, "wait begin", waiting, nil)
	var refused <-chan error
	monitor := watch(o, cmd)
	if monitor != nil {
		refused = monitor.refused
	}
	finished := false
	var result error
	select {
	case result = <-exited:
		finished = true
		recordDiagnostic(o, "wait selected", waiting, result)
	case result = <-refused:
	case <-stop:
		result = errStopped
	case <-timeout:
		result = errStopped
	}
	if !finished {
		recordDiagnostic(o, "wait interrupted", waiting, result)
		output.cancel()
	}
	joining := time.Now()
	recordDiagnostic(o, "group kill begin", joining, result)
	KillGroup(cmd)
	if monitor != nil {
		sampling := time.Now()
		recordDiagnostic(o, "RSS join begin", sampling, nil)
		monitor.stop()
		recordDiagnostic(o, "RSS joined", sampling, nil)
		// Preserve a refusal that raced with leader exit; the publisher is now joined.
		select {
		case result = <-refused:
		default:
		}
	}
	if !finished {
		<-exited
	} // Join the non-reaping observer as well.
	// A cancellation racing exit still cancels any context-aware output writer.
	select {
	case <-stop:
		if result == nil {
			result = errStopped
		}
	default:
	}
	select {
	case <-timeout:
		if result == nil {
			result = errStopped
		}
	default:
	}
	if result != nil {
		output.cancel()
	}
	recordDiagnostic(o, "reap begin", joining, result)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Group signaling and RSS observation are finished forever. Wait owns the
	// reap and output copies; cancellation must join them, without a timer escape.
	select {
	case err = <-done:
	case <-stop:
		if result == nil {
			result = errStopped
		}
		output.cancel()
		err = <-done
	case <-timeout:
		if result == nil {
			result = errStopped
		}
		output.cancel()
		err = <-done
	}
	recordDiagnostic(o, "process joined", joining, err)
	if !finished {
		recordDiagnostic(o, "killed process joined", joining, result)
	}
	if result != nil {
		return result
	}
	return err
}

func recordDiagnostic(o Options, stage string, started time.Time, err error) {
	if o.Diagnostic != nil {
		o.Diagnostic(stage, time.Since(started), err)
	}
}

// Output runs git and returns its trimmed stdout; on failure the error carries stderr's first
// line (never more than 4 KiB of it).
func Output(ctx context.Context, o Options, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := output(ctx.Done(), o, args...)
	if errors.Is(err, errStopped) && ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

// OutputBounded is Output for a call that must not take a caller's context (cleanup on a kill, a
// pod's startup and wip push): it is bounded by o.Timeout alone.
func OutputBounded(o Options, args ...string) (string, error) {
	return output(nil, o, args...)
}

func output(stop <-chan struct{}, o Options, args ...string) (string, error) {
	var out bytes.Buffer
	stderr := &capped{max: 4 << 10}
	o.Stdout, o.Stderr = &out, stderr
	err := run(stop, o, args...)
	if err != nil {
		return strings.TrimSpace(out.String() + "\n" + stderr.buf.String()), err
	}
	return strings.TrimSpace(out.String()), nil
}

type capped struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (w *capped) Write(p []byte) (int, error) {
	if len(p) > w.max-w.buf.Len() {
		w.overflow = true
	}
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

// ProtocolsFor is the one transport a clone or fetch URL needs: https, http, ssh (an ssh:// URL or
// scp-like user@host:path) or file (a local path or file://). Anything else (ext::, fd::, a
// remote helper) gets none.
func ProtocolsFor(rawURL string) []string {
	switch {
	case strings.HasPrefix(rawURL, "https://"):
		return []string{"https"}
	case strings.HasPrefix(rawURL, "http://"):
		return []string{"http"}
	case strings.HasPrefix(rawURL, "ssh://"), strings.HasPrefix(rawURL, "git+ssh://"):
		return []string{"ssh"}
	case strings.HasPrefix(rawURL, "file://"), strings.HasPrefix(rawURL, "/"), strings.HasPrefix(rawURL, "./"), strings.HasPrefix(rawURL, "../"):
		return []string{"file"}
	case strings.Contains(rawURL, "::"):
		return nil
	}
	if at := strings.Index(rawURL, ":"); at > 0 && !strings.Contains(rawURL[:at], "/") {
		return []string{"ssh"} // scp-like: [user@]host:path
	}
	return []string{"file"}
}

// ServeCommand is the --upload-pack or --receive-pack command (service "upload-pack" or
// "receive-pack") for a local transport to a repository someone else writes (a sandboxed session's
// clone, a pod's file-path origin). Git runs a local serving side without our -c overrides or
// environment (it clears them as repository-local variables), so the command carries them itself:
// `<git> --no-replace-objects -c … <service> [--strict]` with the untrusted-repository overrides,
// each argument single-quoted (git runs the command through the shell). Every value is a constant;
// one holding a quote is refused rather than quoted. upload-pack is --strict: it serves that exact
// Git directory only.
func ServeCommand(service string) (string, error) {
	git, err := Binary()
	if err != nil {
		return "", err
	}
	args := append([]string{git, "--no-replace-objects"}, Config(Options{Untrusted: true})...)
	args = append(args, service)
	if service == "upload-pack" {
		args = append(args, "--strict")
	}
	return shellQuote(args)
}

// shellQuote single-quotes every argument; an argument holding a quote is an error, never escaped.
func shellQuote(args []string) (string, error) {
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsRune(a, '\'') {
			return "", errors.New("a serving command argument holds a quote")
		}
		quoted[i] = "'" + a + "'"
	}
	return strings.Join(quoted, " "), nil
}
