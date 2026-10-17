package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceFileAdversarialRegularInodeSwap(t *testing.T) {
	root := t.TempDir()
	file := workspaceFixture(t, root, "readme.md", "PUBLIC!!")
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	got := readWorkspaceFile(root, "readme.md", workspaceFileLimits{beforeOpen: func() {
		if err := os.Rename(file, filepath.Join(root, "original.md")); err != nil {
			t.Fatal(err)
		}
		workspaceFixture(t, root, "readme.md", "PRIVATE!")
		if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
			t.Fatal(err)
		}
	}})
	if got.OK || got.ErrorCode != "forbidden" || got.ContentB64 != "" {
		t.Fatalf("same-size/same-mtime inode swap accepted: %+v", got)
	}
}

func TestWorkspaceFileAdversarialChangesAfterOpen(t *testing.T) {
	for _, kind := range []string{"new hardlink", "final replacement", "root replacement", "parent replacement", "parent secret alias"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "workspace")
			file := workspaceFixture(t, root, "docs/readme.md", "PUBLIC!!")
			before, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			got := readWorkspaceFile(root, "docs/readme.md", workspaceFileLimits{afterOpen: func() {
				switch kind {
				case "new hardlink":
					if err := os.Link(file, filepath.Join(root, "other.md")); err != nil {
						t.Fatal(err)
					}
				case "final replacement":
					if err := os.Rename(file, filepath.Join(root, "original.md")); err != nil {
						t.Fatal(err)
					}
					workspaceFixture(t, root, "docs/readme.md", "PRIVATE!")
					if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
						t.Fatal(err)
					}
				case "root replacement":
					if err := os.Rename(root, root+"-original"); err != nil {
						t.Fatal(err)
					}
					workspaceFixture(t, root, "docs/readme.md", "PRIVATE!")
				case "parent replacement":
					if err := os.Rename(filepath.Join(root, "docs"), filepath.Join(root, "original")); err != nil {
						t.Fatal(err)
					}
					workspaceFixture(t, root, "docs/readme.md", "PRIVATE!")
				case "parent secret alias":
					if err := os.Rename(filepath.Join(root, "docs"), filepath.Join(root, ".codex")); err != nil {
						t.Fatal(err)
					}
					workspaceLink(t, ".codex", filepath.Join(root, "docs"))
				}
			}})
			if got.OK || got.ErrorCode != "forbidden" || got.ContentB64 != "" {
				t.Fatalf("post-open mutation accepted: %+v", got)
			}
		})
	}
}

func TestWorkspaceFileAdversarialIntermediateSecretBeforeParentTraversal(t *testing.T) {
	root := t.TempDir()
	workspaceFixture(t, root, "public.md", "public fixture")
	workspaceFixture(t, root, ".aws/config", "private fixture")
	workspaceFixture(t, root, "docs/placeholder", "public fixture")
	workspaceLink(t, ".aws", filepath.Join(root, "hidden"))
	workspaceLink(t, "hidden/../public.md", filepath.Join(root, "alias.md"))
	if got := readWorkspaceFile(root, "alias.md", workspaceFileLimits{}); got.OK || got.ErrorCode != "forbidden" || got.ContentB64 != "" {
		t.Fatalf("protected intermediate target erased by cleaning: %+v", got)
	}
	// A confined parent traversal in a benign symlink target remains usable.
	workspaceLink(t, "../public.md", filepath.Join(root, "docs", "safe.md"))
	workspaceLink(t, "docs/safe.md", filepath.Join(root, "safe-chain.md"))
	if got := readWorkspaceFile(root, "safe-chain.md", workspaceFileLimits{}); !got.OK {
		t.Fatalf("ordinary confined link chain rejected: %+v", got)
	}
}
