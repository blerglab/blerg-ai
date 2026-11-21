//go:build linux

package shellruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
	"golang.org/x/sys/unix"
)

func TestCgroupCeilingBeforeStart(t *testing.T) {
	if os.Args[len(os.Args)-1] != "ceiling-proof" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCgroupCeilingBeforeStart$", "--", "ceiling-proof")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ceiling proof: %v\n%s", err, output)
		}
		return
	}
	tree := newCgroupTree("")
	sentinel := errors.New("stop after observing startup")
	ready := make(chan error, 1)
	cmd := exec.Command("/bin/bash", "-c", "ulimit -H -u")
	_, err := tree.Run(cmd, func() error {
		var inherited unix.Rlimit
		if e := unix.Getrlimit(unix.RLIMIT_NPROC, &inherited); e != nil {
			t.Fatal(e)
		}
		if tree.hard == 0 || inherited.Max != tree.hard {
			t.Fatalf("first exec can inherit %d, want ceiling %d", inherited.Max, tree.hard)
		}
		return sentinel
	}, TasksPerShell, ready)
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestCgroupOwnerDeathNeedsEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, running string
		err           error
		dead          bool
	}{
		{name: "live", running: "123"},
		{name: "reused", running: "124", dead: true},
		{name: "missing", err: os.ErrNotExist, dead: true},
		{name: "denied", err: os.ErrPermission},
		{name: "io", err: unix.EIO},
		{name: "malformed", err: errors.New("short process status")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dead, err := shellCgroupOwnerDead(42, "123", func(int) (string, error) { return tc.running, tc.err })
			if dead != tc.dead {
				t.Fatalf("dead=%v, want %v", dead, tc.dead)
			}
			if tc.err != nil && !tc.dead && err == nil {
				t.Fatal("lost owner observation error")
			}
		})
	}
}

func TestCgroupControlMissingProofIsUnproven(t *testing.T) {
	for _, op := range []string{"freeze", "thaw"} {
		t.Run(op, func(t *testing.T) {
			tree := newCgroupTree(filepath.Join(t.TempDir(), "missing"))
			close(tree.done)
			if err := tree.Control(context.Background(), op); !errors.Is(err, processcontainment.ErrUnproven) {
				t.Fatalf("missing proof answered %v", err)
			}
		})
	}
}

func TestCgroupProcScanDoesNotHideReadErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "42"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []error{os.ErrPermission, unix.EIO, errors.New("short process status")} {
		if _, err := procTasksFrom(dir, func(int) (procTask, error) { return procTask{}, failure }); !errors.Is(err, failure) {
			t.Fatalf("scan hid %v: %v", failure, err)
		}
	}
	for _, failure := range []error{os.ErrNotExist, unix.ESRCH} {
		if tasks, err := procTasksFrom(dir, func(int) (procTask, error) { return procTask{}, failure }); err != nil || len(tasks) != 0 {
			t.Fatalf("exited task: %v %v", tasks, err)
		}
	}
}

func TestRemoveEmptyShellCgroupParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeEmptyShellCgroupParent(parent); err == nil {
		t.Fatal("removed nonempty parent")
	}
	if err := os.Remove(child); err != nil {
		t.Fatal(err)
	}
	if err := removeEmptyShellCgroupParent(parent); err != nil {
		t.Fatal(err)
	}
	if err := removeEmptyShellCgroupParent(parent); err != nil {
		t.Fatal(err)
	}
}
