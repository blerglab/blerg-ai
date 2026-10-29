//go:build darwin

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A private disk image creates a real other-device boundary without touching
// any system mount. Only this test's image and attachment are removed.
func TestWorkspaceExplorerOtherDeviceMount(t *testing.T) {
	e, root, req := explorerFixture(t)
	image := filepath.Join(t.TempDir(), "boundary.dmg")
	mount := filepath.Join(root, "mount")
	if err := os.Mkdir(mount, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("hdiutil", args...).CombinedOutput(); err != nil {
			t.Fatalf("private image: %v %s", err, out)
		}
	}
	run("create", "-size", "16m", "-fs", "HFS+", "-volname", "WorkspaceBoundary", "-ov", image)
	run("attach", "-nobrowse", "-noverify", "-mountpoint", mount, image)
	defer run("detach", mount)
	writeExplorerFile(t, mount, "outside.txt")
	req.Path = "mount"
	if res := e.Explore(t.Context(), req); res.OK {
		t.Fatal("other-device directory accepted")
	}
	req.Path = ""
	req.Operation = "search"
	req.Filter = "outside"
	if res := e.Explore(t.Context(), req); len(res.Entries) > 0 {
		t.Fatal("search crossed device")
	}
}
