//go:build linux || darwin

package discovery

import (
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializeHostFolderWithoutStagingFiles(t *testing.T) {
	root := fixtureRoot(t)
	folder := filepath.Join(root, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "notes.txt"), []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	entry := find(t, list(t, s, "."), "project")
	req := request("initialize")
	ref := ref(entry)
	req.Folder = &ref
	res := s.Handle(t.Context(), req)
	if !res.OK || res.Resolved == nil || len(res.Resolved.Members) != 1 {
		t.Fatalf("initialize: %+v", res)
	}
	parent := manifestForSelection(res.Resolved, "initialized-parent")
	bind := request("bind")
	bind.SelectionIdentity = res.Resolved.SelectionIdentity
	bind.Manifest = &parent
	if reply := s.Handle(t.Context(), bind); !reply.OK {
		t.Fatal("initialized parent did not bind", reply.ErrorCode)
	}
	child := parent
	child.LaunchGeneration = "initialized-child"
	child.Digest = workspaceselection.Digest(child)
	bind = request("bind_child")
	bind.ParentLaunchGeneration = parent.LaunchGeneration
	bind.Manifest = &child
	if reply := s.Handle(t.Context(), bind); !reply.OK {
		t.Fatal("initialized folder cannot bind host child", reply.ErrorCode)
	}
	if got := fixtureGit(t, folder, "ls-tree", "--name-only", "HEAD"); got != "" {
		t.Fatalf("staged personal files: %s", got)
	}
	if got := fixtureGit(t, folder, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("commit count: %s", got)
	}
	if raw, err := os.ReadFile(filepath.Join(folder, ".git", "config")); err != nil || string(raw) == "" || strings.Contains(string(raw), "[user]") {
		t.Fatalf("config: %v", err)
	}
	if got := fixtureGit(t, folder, "status", "--porcelain"); got != "?? notes.txt" {
		t.Fatalf("personal file changed: %s", got)
	}
}

func TestInitializeRefusesExistingWorktreeAndSandbox(t *testing.T) {
	for _, mode := range []string{"nested", "existing", "sandbox", "cluster"} {
		t.Run(mode, func(t *testing.T) {
			root := fixtureRoot(t)
			fixtureRepo(t, root, "project", true)
			rel := "project"
			if mode == "nested" {
				rel = "project/nested"
				if err := os.Mkdir(filepath.Join(root, rel), 0700); err != nil {
					t.Fatal(err)
				}
			}
			s := New(func() string { return root })
			req := request("initialize")
			if mode == "nested" || mode == "existing" {
				e := find(t, list(t, s, filepath.ToSlash(filepath.Dir(rel))), rel)
				r := ref(e)
				req.Folder = &r
			} else {
				req.Path = "new-project"
			}
			if mode == "sandbox" {
				req.Runtime = "sandbox"
			}
			if mode == "cluster" {
				req.Runtime = "cluster"
			}
			if res := s.Handle(t.Context(), req); res.OK {
				t.Fatal("unsafe initialization accepted")
			}
		})
	}
}

func TestInitializeConfiguredRoot(t *testing.T) {
	root := fixtureRoot(t)
	s := New(func() string { return root })
	page := list(t, s, ".")
	r := ref(page.Root)
	req := request("initialize")
	req.Folder = &r
	result := s.Handle(t.Context(), req)
	if !result.OK || result.Resolved == nil || len(result.Resolved.Members) != 1 {
		t.Fatalf("root initialization: %+v", result)
	}
	if got := fixtureGit(t, root, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatal(got)
	}
}
