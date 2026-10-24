package hostgit

import (
	"os/exec"

	"golang.org/x/sys/unix"
)

func platformStartCommand(o Options, cmd *exec.Cmd) error {
	return startInDirectory(cmd, o.DirFile, unix.PthreadFchdir)
}
