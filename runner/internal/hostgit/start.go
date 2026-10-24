package hostgit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Darwin's /dev/fd/N is an open/dup interface, not a searchable directory path.
// Set a thread-local cwd from the held descriptor, then fork on that same thread.
// Darwin inherits the calling thread's cwd as the child's process cwd. The
// goroutine exits locked, so Go retires the thread: no changed cwd can escape
// into unrelated goroutines, even when setup/exec fails. Linux tests fake chdir.
func startInDirectory(cmd *exec.Cmd, directory *os.File, chdir func(int) error) error {
	if directory == nil {
		return cmd.Start()
	}
	if cmd.Dir != "" {
		return errors.New("pinned Git cwd requires an empty command Dir")
	}
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // Intentionally never unlocked: this thread owns a private cwd.
		if err := chdir(int(directory.Fd())); err != nil {
			started <- fmt.Errorf("set pinned Git cwd: %w", err)
			return
		}
		started <- cmd.Start()
	}()
	err := <-started
	runtime.KeepAlive(directory)
	return err
}
