//go:build linux

package processcontainment

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	shimPath   = "/proc/self/exe" // the running image, even if an upgrade replaced the file
	shimMarker = "blerg-containment-shim"
)

// The shim is the first image of a tree's root. RET_TRACE answers ENOSYS unless a tracer has set
// TRACESECCOMP, and only the task itself can install a filter, so the root must install the exec
// filter, wait to be seized, and only then exec: it cannot be done from a root that execs its target
// directly. The hook runs in every binary that can reach Tree.Run, before main, on the main thread.
func init() {
	if len(os.Args) > 3 && os.Args[1] == shimMarker {
		os.Exit(runShim(os.Args[2], os.Args[3:]))
	}
}

// shimFailed is the shim's exit status when it could not reach exec. Exec failures exit with their
// errno, which is never this value.
const shimFailed = 255

func runShim(path string, argv []string) int {
	// Starts the runtime's template thread, so a thread it needs from here on is not this one's child
	// (seized, and counted against a small task limit).
	runtime.LockOSThread()
	if installExecFilter() != nil {
		return shimFailed
	}
	// A directed signal: this thread is stopped before tgkill returns. The tracer seizes it here; a
	// stray SIGCONT lets it reach exec unseized, which the filter fails with ENOSYS.
	if unix.Tgkill(unix.Getpid(), unix.Gettid(), unix.SIGSTOP) != nil {
		return shimFailed
	}
	var errno syscall.Errno
	if errors.As(syscall.Exec(path, argv, os.Environ()), &errno) { //nolint:gosec // the command the tracer was told to run
		return int(errno)
	}
	return shimFailed
}

// throughShim makes cmd start the shim, which then execs what cmd named. A command that could not
// resolve its path keeps it: Start reports that as before.
func throughShim(cmd *exec.Cmd) {
	if cmd.Err != nil {
		return
	}
	argv := cmd.Args
	if len(argv) == 0 {
		argv = []string{cmd.Path}
	}
	cmd.Args = append([]string{shimPath, shimMarker, cmd.Path}, argv...)
	cmd.Path = shimPath
}
