package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
