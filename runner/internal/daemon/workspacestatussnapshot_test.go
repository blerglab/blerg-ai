package daemon

import (
	"crypto/sha1"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
)

// Use the Darwin snapshot/config/env path with actual Git on Linux as well as
// Darwin. Native tests separately exercise the descriptor-cwd start primitive.
func TestWorkspaceStatusDarwinSnapshot(t *testing.T) {
	for _, aliasTemp := range []bool{false, true} {
		t.Run(map[bool]string{false: "physical-temp", true: "aliased-temp"}[aliasTemp], func(t *testing.T) {
			e, root, _ := explorerFixture(t)
			member := filepath.Join(root, "member")
			initChildRepo(t, member)
			e.pinMembers()
			if aliasTemp {
				base := t.TempDir()
				physical := filepath.Join(base, "physical")
				alias := filepath.Join(base, "alias")
				if err := os.Mkdir(physical, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(physical, alias); err != nil {
					t.Fatal(err)
				}
				t.Setenv("TMPDIR", alias)
			}
			snapshot, err := e.statusSnapshotForPlatform(t.Context(), "member", hostWorktreePin{}, false, "darwin")
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.close()
			info, err := os.Stat(member)
			if err != nil {
				t.Fatal(err)
			}
			p, ok := pinGitDir(member, info)
			if !ok {
				t.Fatal("could not pin fixture cwd")
			}
			defer p.close()
			writeExplorerFile(t, member, "new.txt")
			out, stderr := &cappedOutput{max: 1 << 20}, &cappedOutput{max: 4 << 10}
			o := snapshot.optionsForPlatform(p, out, "darwin")
			o.Stderr = stderr
			if o.DirFile != p.dir || !strings.Contains(strings.Join(o.Env, "\n"), "GIT_OBJECT_DIRECTORY="+filepath.Join(snapshot.directory, "objects")) {
				t.Fatalf("Darwin snapshot lost pinned cwd/copied objects: %+v", o)
			}
			for _, variable := range o.Env {
				if strings.Contains(variable, "/proc/") || strings.Contains(variable, "/dev/fd/") {
					t.Fatalf("Darwin environment uses unsupported descriptor traversal: %s", variable)
				}
			}
			if err := hostgit.Run(t.Context(), o, "status", "--porcelain=v2", "-z", "--branch", "--no-renames", "--ignore-submodules=all"); err != nil {
				t.Fatalf("Darwin snapshot Git: %v stderr=%q", err, stderr.buf.String())
			}
			status, err := parseWorkspaceStatus(out.buf.String())
			if err != nil || status.Untracked != 1 {
				t.Fatalf("Darwin snapshot result: %+v %v", status, err)
			}
		})
	}
}

// Model a racy index without relying on filesystem clock resolution or retries:
// retain the old blob but give its entry the edited file's exact cached stat.
func TestWorkspaceExplorerStatusRacyIndex(t *testing.T) {
	e, root, req := explorerFixture(t)
	member := filepath.Join(root, "member")
	initChildRepo(t, member)
	tracked := filepath.Join(member, "initial.txt")
	index := filepath.Join(member, ".git", "index")
	original, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if string(original[:4]) != "DIRC" || binary.BigEndian.Uint32(original[4:8]) != 2 || binary.BigEndian.Uint32(original[8:12]) != 1 {
		t.Fatal("fixture requires one SHA-1 index v2 entry")
	}
	if err := os.WriteFile(tracked, []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 123456789)
	if err := os.Chtimes(tracked, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	// Ask Git to encode platform-specific stat fields; keep only those fields,
	// never the new blob. This avoids platform-specific struct stat assumptions.
	childRepoGit(t, member, "add", "initial.txt")
	edited, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	copy(original[12:52], edited[12:52])
	checksum := sha1.Sum(original[:len(original)-sha1.Size])
	copy(original[len(original)-sha1.Size:], checksum[:])
	if err := os.WriteFile(index, original, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(index, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	e.pinMembers()
	req.Operation, req.Path, req.MemberID = "status", "member", "one"
	result := e.Explore(t.Context(), req)
	if !result.OK || result.Status == nil || result.Status.Changed != 1 {
		t.Fatalf("racy same-size edit must be changed: %+v", result)
	}
	snapshot, err := e.statusSnapshot(t.Context(), "member", hostWorktreePin{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	copied, err := os.Stat(filepath.Join(snapshot.directory, "index"))
	if err != nil {
		t.Fatal(err)
	}
	if !copied.ModTime().Equal(info.ModTime()) {
		t.Fatalf("index timestamp lost precision: got %v, want %v", copied.ModTime(), info.ModTime())
	}
}
