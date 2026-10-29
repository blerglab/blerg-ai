//go:build linux

package clusterworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func accountingFixture(t *testing.T) (*DiskGit, string) {
	t.Helper()
	root := t.TempDir()
	for _, id := range []string{"one", "two"} {
		if err := os.Mkdir(filepath.Join(root, id), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRead(f) })
	return &DiskGit{root: f, members: map[string]*diskMember{"one": {path: "one"}, "two": {path: "two"}}}, root
}
func TestClusterRefusedDeepMemberStillAccountsAggregate(t *testing.T) {
	for _, kind := range []string{"bytes", "entries"} {
		t.Run(kind, func(t *testing.T) {
			d, root := accountingFixture(t)
			deep := filepath.Join(root, "two")
			for range 130 {
				deep = filepath.Join(deep, "d")
			}
			if err := os.MkdirAll(deep, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(deep, "payload")
			f, err := os.Create(file)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "bytes" {
				if err := f.Truncate(11 << 30); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "entries" {
				// Hard links consume directory entries without filling the shared disk.
				for i := range 500000 {
					if i%60000 == 0 && i != 0 {
						file = filepath.Join(deep, fmt.Sprintf("payload-%d", i))
						extra, err := os.Create(file)
						if err != nil {
							t.Fatal(err)
						}
						if err := extra.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Link(file, filepath.Join(deep, fmt.Sprint(i))); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			refusals := map[string]error{}
			if err := d.scanBudget(ctx, refusals); !errors.Is(err, errAggregateBudget) {
				t.Fatal("partial member accounting accepted hidden exhaustion", err, refusals)
			}
			d.BeginRound()
			if blocked := d.ValidateRound(ctx); blocked["one"] == nil || blocked["two"] == nil {
				t.Fatal("aggregate exhaustion did not block both members", blocked)
			}
		})
	}
}
func TestClusterUnreadableMemberCannotAuthorizePartialBudget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires nonprivileged host UID")
	}
	d, root := accountingFixture(t)
	hidden := filepath.Join(root, "two", "hidden")
	if err := os.Mkdir(hidden, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(hidden, "payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(11 << 30); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Chmod(hidden, 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(hidden, 0700) }()
	if blocked := d.ValidateRound(context.Background()); blocked["one"] == nil || blocked["two"] == nil {
		t.Fatal("unreadable usage treated as zero", blocked)
	}
}
func TestClusterMountRefusalCannotAuthorizePartialBudget(t *testing.T) {
	if os.Getenv("P3_ACCOUNTING_MOUNT_CHILD") != "1" {
		if os.Geteuid() == 0 {
			t.Skip("requires nonprivileged host UID")
		}
		if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
			t.Skipf("user/mount namespace fixture unavailable: %v", err)
		}
		// Mount only in a new user/mount namespace; no host privilege or global mount.
		cmd := exec.Command("unshare", "--user", "--map-root-user", "--mount", os.Args[0], "-test.run=^TestClusterMountRefusalCannotAuthorizePartialBudget$")
		cmd.Env = append(os.Environ(), "P3_ACCOUNTING_MOUNT_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("nonprivileged mount fixture: %v: %s", err, out)
		}
		return
	}
	d, root := accountingFixture(t)
	mount := filepath.Join(root, "two", "mounted")
	if err := os.Mkdir(mount, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", mount, "tmpfs", 0, "size=1m"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Unmount(mount, 0); err != nil {
			t.Error(err)
		}
	}()
	if blocked := d.ValidateRound(context.Background()); blocked["one"] == nil || blocked["two"] == nil {
		t.Fatal("cross-mount accounting treated as complete", blocked)
	}
}

func TestClusterAccountingDepthCeilingFailsWholeRound(t *testing.T) {
	d, root := accountingFixture(t)
	deep := filepath.Join(root, "two")
	for range 257 {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0700); err != nil {
		t.Fatal(err)
	}
	if blocked := d.ValidateRound(context.Background()); blocked["one"] == nil || blocked["two"] == nil {
		t.Fatal("unaccounted deep usage treated as zero", blocked)
	}
}
