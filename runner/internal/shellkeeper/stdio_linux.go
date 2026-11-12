//go:build linux

package shellkeeper

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"golang.org/x/sys/unix"
)

// Stdio runs one helper for the extraction slice, with inherited private
// descriptors rather than a byte-copying proxy. The keeper is still owned by
// the daemon in this mode; this does not advertise restart survival. There is
// no caller-supplied executable, environment or argument vector.
func Stdio() int {
	return stdio(StartOwned)
}

func stdio(start func(*exec.Cmd, <-chan struct{}) error) int {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return failed("protect", err)
	}
	cmd := exec.Command("/proc/self/exe", "shell-supervisor") //nolint:noctx // pinned running image, keeper owns lifetime
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	done := make(chan struct{})
	if err := start(cmd, done); err != nil {
		return failed("helper_start", err)
	}
	err := cmd.Wait()
	close(done)
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3 {
		failurelog.Default.Event("shell_keeper.identity_changed", slog.LevelWarn, "component", "shell_keeper", "stage", "helper_wait", "cause", "identity_changed")
		return 3
	}
	return failed("helper_wait", err)
}

func failed(stage string, err error) int {
	failurelog.Default.Event("shell_keeper."+stage, slog.LevelError, "component", "shell_keeper", "stage", stage, "cause", failurelog.Cause(err))
	return 1
}
