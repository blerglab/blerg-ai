//go:build linux

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// These tests execute the Linux-only ext4/loop-mount quota backend. The guest
// entry is invoked by a Linux test executable inside the privileged Docker
// namespace; the launcher below mounts that same executable into the guest.
// They cannot run in a native macOS process, even with Docker Desktop present.

// Docker guest entry for actual host-filesystem quotas. The daemon backend and
// arbitrary writer run in a privileged test mount namespace; no host mount is
// changed. The filesystem image and journal survive backend reconstruction.
func TestWorkspaceQuotaGuest(t *testing.T) {
	if os.Getenv("BLERG_P2_QUOTA_GUEST") != "1" {
		t.Skip("Docker guest only")
	}
	root := t.TempDir()
	storage := &FilesystemWorkspaceStorage{Root: filepath.Join(root, "allocations")}
	budget := smallWorkspaceQuotaBudget()
	tree, err := storage.Allocate(t.Context(), "quota-guest", budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := storage.Release(context.Background(), "quota-guest", tree); err != nil {
			t.Error(err)
		}
	})
	// A separate process writes arbitrary bytes, independently of the daemon.
	writer := exec.Command("python3", "-I", "-S", "-c", `import os,sys,errno
p=sys.argv[1]
try:
 with open(p+'/large','wb',buffering=0) as f:
  for n in range(1024): f.write(b'x'*65536)
 raise AssertionError('byte limit missing')
except OSError as e: assert e.errno==errno.ENOSPC
os.unlink(p+'/large')
open(p+'/one','w').close()
try:
 os.link(p+'/one',p+'/alias')
 raise AssertionError('hardlink entry bypass')
except OSError as e: assert e.errno==errno.EXDEV
os.unlink(p+'/one')
try:
 for n in range(4096): open(p+'/'+str(n),'w').close()
 raise AssertionError('inode limit missing')
except OSError as e: assert e.errno==errno.ENOSPC
`, tree)
	if out, err := writer.CombinedOutput(); err != nil {
		t.Fatalf("arbitrary writer bypassed quota: %v %s", err, out)
	}
	restored := &FilesystemWorkspaceStorage{Root: storage.Root}
	if err := restored.Validate(t.Context(), "quota-guest", tree, budget); err != nil {
		t.Fatal("quota authority lost on restart:", err)
	}
	wrong := budget
	wrong.CheckoutBytes++
	if err := restored.Validate(t.Context(), "quota-guest", tree, wrong); err == nil {
		t.Fatal("changed budget accepted")
	}
	if err := restored.Release(t.Context(), "quota-guest", tree); err != nil {
		t.Fatal(err)
	}
	if err := restored.Release(t.Context(), "quota-guest", tree); err != nil {
		t.Fatal("release not idempotent:", err)
	}
}

func TestP2DockerPersistentHostFilesystemQuota(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("set BLERG_P2_DOCKER_TEST=1")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	label := "p2-quota-fix-" + ccUUID()
	// --rm plus exact unique label cleanup covers a lost client acknowledgement.
	t.Cleanup(func() { cleanupP2Containers(t, label) })
	entryPolicy, err := workspaceEntrySecurityOption()
	if err != nil {
		t.Fatal(err)
	}
	p2Docker(t, "run", "--rm", "--pull=never", "--security-opt", entryPolicy, "--privileged", "--user", "0:0", "--network=none", "--label", "blerg-test-run="+label,
		"--mount", sandboxBindMount(binary, "/quota-test")+",readonly", "--env", "BLERG_P2_QUOTA_GUEST=1", "--tmpfs", "/tmp:rw,size=268435456,nr_inodes=16384,mode=1777",
		"--entrypoint", "/quota-test", sandboxImage(), "-test.run=^TestWorkspaceQuotaGuest$", "-test.v")
}
func smallWorkspaceQuotaBudget() protocol.WorkspaceBudget {
	b := protocol.DefaultWorkspaceBudget()
	b.CheckoutBytes = 16 << 20
	b.CheckoutEntries = 512
	return b
}
