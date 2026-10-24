//go:build darwin || linux

package hostgit

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestDarwinDirectoryCommand(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	cmd, err := commandForPlatform(Options{Dir: "/dev/fd/42", DirFile: directory, ExtraFiles: []*os.File{directory}, MemoryLimit: true}, "darwin", "--version")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != "" {
		t.Fatalf("Darwin still attempts chdir through fdescfs: %q", cmd.Dir)
	}
}

func TestDarwinDirectoryStart(t *testing.T) {
	for _, denied := range []bool{false, true} {
		directory, err := os.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		called := false
		err = startInDirectory(cmd, directory, func(fd int) error {
			called = true
			if fd != int(directory.Fd()) {
				t.Errorf("wrong cwd descriptor: %d", fd)
			}
			if denied {
				return syscall.EACCES
			}
			return nil
		})
		if cmd.Process != nil {
			_ = cmd.Wait()
		}
		_ = directory.Close()
		if !called || denied && (!errors.Is(err, syscall.EACCES) || cmd.Process != nil) || !denied && err != nil {
			t.Fatalf("pinned cwd setup skipped or failure ignored: called=%v denied=%v process=%v err=%v", called, denied, cmd.Process, err)
		}
	}
}
