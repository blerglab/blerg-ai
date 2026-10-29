//go:build linux

package shellruntime

import (
	"os/exec"
	"runtime"
	"syscall"
)

// Linux parent-death signals bind to the creating THREAD, not just its process.
// Keep that thread alive until Wait finishes so idle Go thread retirement cannot
// kill a healthy helper. Daemon death kills its owned helper/client immediately.
func startOwnedCommand(cmd *exec.Cmd, done <-chan struct{}) error {
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		err := cmd.Start()
		result <- err
		if err == nil {
			<-done
		}
	}()
	return <-result
}
