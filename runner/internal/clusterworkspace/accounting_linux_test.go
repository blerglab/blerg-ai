//go:build linux

package clusterworkspace

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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
			d := &DiskGit{members: map[string]*diskMember{"one": {path: "one"}, "two": {path: "two"}}}
			d.budgetRoot = func() (budgetDirectory, error) { return &accountingStream{kind: kind}, nil }
			refusals := map[string]error{}
			if err := d.scanBudget(t.Context(), refusals); !errors.Is(err, errAggregateBudget) || !errors.Is(refusals["two"], ErrPlan) {
				t.Fatal("partial member accounting accepted hidden exhaustion", err, refusals)
			}
			d.BeginRound()
			if blocked := d.ValidateRound(t.Context()); blocked["one"] == nil || blocked["two"] == nil {
				t.Fatal("aggregate exhaustion did not block both members", blocked)
			}
		})
	}
}

// A bounded stream supplies the same 130 directories and 500,001 payload
// entries without making the filesystem materialize the policy matrix.
type accountingStream struct {
	kind           string
	depth, emitted int
}

func (s *accountingStream) ReadDir(n int) ([]os.DirEntry, error) {
	total := 1
	if s.depth == 131 && s.kind == "entries" {
		total = 500001
	}
	if s.emitted == total {
		return nil, io.EOF
	}
	count := min(n, total-s.emitted)
	out := make([]os.DirEntry, count)
	name := "d"
	if s.depth == 0 {
		name = "two"
	}
	if s.depth == 131 {
		name = "payload"
	}
	for i := range out {
		out[i] = accountingEntry{name: name, directory: s.depth < 131}
	}
	s.emitted += count
	return out, nil
}
func (s *accountingStream) Stat(string) (unix.Stat_t, error) {
	if s.depth < 131 {
		return unix.Stat_t{Mode: unix.S_IFDIR}, nil
	}
	size := int64(0)
	if s.kind == "bytes" {
		size = 11 << 30
	}
	return unix.Stat_t{Mode: unix.S_IFREG, Size: size}, nil
}
func (s *accountingStream) Open(string) (budgetDirectory, error) {
	return &accountingStream{kind: s.kind, depth: s.depth + 1}, nil
}
func (*accountingStream) Close() {}

type accountingEntry struct {
	name      string
	directory bool
}

func (e accountingEntry) Name() string { return e.name }
func (e accountingEntry) IsDir() bool  { return e.directory }
func (e accountingEntry) Type() os.FileMode {
	if e.directory {
		return os.ModeDir
	}
	return 0
}
func (e accountingEntry) Info() (os.FileInfo, error) { return nil, errors.New("unexpected Info call") }

// Keep the real descriptor traversal, depth refusal, sparse stat and hard-link
// accounting path: two names for one inode must each consume logical bytes.
func TestClusterDeepHardLinksAccountAggregateOnDisk(t *testing.T) {
	d, root := accountingFixture(t)
	deep := filepath.Join(root, "two")
	for range 130 {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0700); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(deep, "payload")
	f, err := os.Create(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(6 << 30); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	refusals := map[string]error{}
	if err := d.scanBudget(t.Context(), refusals); err != nil || !errors.Is(refusals["two"], ErrPlan) {
		t.Fatal("small refused member must still be fully accounted", err, refusals)
	}
	if err := os.Link(payload, filepath.Join(deep, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := d.scanBudget(t.Context(), map[string]error{}); !errors.Is(err, errAggregateBudget) {
		t.Fatal("hard-link usage omitted", err)
	}
	d.BeginRound()
	if blocked := d.ValidateRound(t.Context()); blocked["one"] == nil || blocked["two"] == nil {
		t.Fatal("aggregate exhaustion did not block both members", blocked)
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
