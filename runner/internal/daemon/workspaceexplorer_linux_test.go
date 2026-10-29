//go:build linux

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWorkspaceExplorerSameDeviceBindMount(t *testing.T) {
	e, root, req := explorerFixture(t)
	source := t.TempDir()
	writeExplorerFile(t, source, "outside.txt")
	target := filepath.Join(root, "mount")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	// Only this test's mount is ever unmounted. No shared/container cleanup.
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Skipf("bind-mount gate requires CAP_SYS_ADMIN: %v", err)
	}
	defer func() {
		if err := unix.Unmount(target, 0); err != nil {
			t.Errorf("unmount own bind: %v", err)
		}
	}()
	req.Path = "mount"
	if result := e.Explore(context.Background(), req); result.OK {
		t.Fatal("same-device bind mount traversed")
	}
	req.Path = ""
	req.Operation = "search"
	req.Filter = "outside"
	if result := e.Explore(context.Background(), req); len(result.Entries) != 0 {
		t.Fatal("search crossed bind mount")
	}
}

func TestWorkspaceExplorerOtherDeviceMount(t *testing.T) {
	e, root, req := explorerFixture(t)
	target := filepath.Join(root, "mount")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "size=1m"); err != nil {
		t.Skipf("mount gate requires CAP_SYS_ADMIN: %v", err)
	}
	defer func() {
		if err := unix.Unmount(target, 0); err != nil {
			t.Errorf("unmount own tmpfs: %v", err)
		}
	}()
	writeExplorerFile(t, target, "outside.txt")
	req.Path = "mount"
	if result := e.Explore(context.Background(), req); result.OK {
		t.Fatal("other-device mount traversed")
	}
}
