//go:build linux

// Package shellkeeper owns shell helper supervision, never ptrace containment.
package shellkeeper

import (
	"os/exec"
	"runtime"
	"syscall"
)

// StartOwned binds a child to its creating thread, keeping that thread alive
// until the caller has reaped the child and closed done. Linux Pdeathsig targets
// a thread, not a process: retiring an idle Go thread must not kill a shell.
// Both the daemon's transitional children and the keeper's helpers use this.
func StartOwned(cmd *exec.Cmd, done <-chan struct{}) error {
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
		err := cmd.Start()
		result <- err
		if err == nil {
			<-done
		}
	}()
	return <-result
}
