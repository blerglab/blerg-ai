package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
)

func TestWorkspaceStatusSiblingWorktrees(t *testing.T) {
	for _, count := range []int{1, 50, 64} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			e, root, req := explorerFixture(t)
			repo := filepath.Join(root, "member")
			initChildRepo(t, repo)
			for i := 0; i < count; i++ {
				childRepoGit(t, repo, "worktree", "add", "-q", "--detach", filepath.Join(root, fmt.Sprintf("sibling-%d", i)))
			}
			e.pinMembers()
			writeExplorerFile(t, repo, "new.txt")
			req.Operation, req.Path, req.MemberID = "status", "member", "one"
			res := e.Explore(t.Context(), req)
			if !res.OK || res.Status == nil || res.Status.Branch != "main" || res.Status.Untracked != 1 || res.Status.Paths[0] != "new.txt" {
				t.Fatalf("status with %d worktrees: %+v", count, res)
			}
			before, safe := e.statusMetadata(t.Context(), "member/.git", "")
			if !safe {
				t.Fatal("worktree records refused")
			}
			pointer := filepath.Join(repo, ".git", "worktrees", "sibling-0", "gitdir")
			writeStatusFixture(t, pointer, "/nonexistent/changed-worktree/.git\n")
			after, safe := e.statusMetadata(t.Context(), "member/.git", "")
			if !safe || before == after {
				t.Fatal("inert pointer change was not fingerprinted")
			}
			// The changed fingerprint must bypass the five-second status cache.
			runs := 0
			ctx := context.WithValue(t.Context(), workspaceStatusTimingKey{}, workspaceStatusTiming(func(stage string, _, _ time.Duration, _ error) {
				if stage == "Git status" {
					runs++
				}
			}))
			if res = e.Explore(ctx, req); !res.OK || runs != 1 {
				t.Fatalf("changed record reused cache: runs=%d result=%+v", runs, res)
			}
		})
	}
}

func writeStatusFixture(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceStatusSiblingWorktreeHostileTarget(t *testing.T) {
	e, root, req := explorerFixture(t)
	repo := filepath.Join(root, "member")
	initChildRepo(t, repo)
	childRepoGit(t, repo, "worktree", "add", "-q", "--detach", filepath.Join(root, "sibling"))
	hostile := t.TempDir()
	initChildRepo(t, hostile)
	marker := filepath.Join(hostile, "hook-ran")
	hook := filepath.Join(hostile, "monitor")
	writeStatusFixture(t, hook, "#!/bin/sh\ntouch '"+marker+"'\n")
	if err := os.Chmod(hook, 0700); err != nil {
		t.Fatal(err)
	}
	// Invalid included config makes even reading the target fail. The monitor
	// also catches execution if the configuration is used without the include.
	writeStatusFixture(t, filepath.Join(hostile, "invalid-config"), "[invalid\n")
	writeStatusFixture(t, filepath.Join(hostile, ".git", "config"), "[core]\nrepositoryformatversion=0\nbare=false\nfsmonitor="+hook+"\nhooksPath="+hostile+"\n[include]\npath="+filepath.Join(hostile, "invalid-config")+"\n")
	for _, pointer := range []string{"gitdir", "commondir"} {
		writeStatusFixture(t, filepath.Join(repo, ".git", "worktrees", "sibling", pointer), filepath.Join(hostile, ".git")+"\n")
	}
	e.pinMembers()
	writeExplorerFile(t, repo, "new.txt")
	// No inherited Git variable may redirect the private invocation either.
	for _, variable := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY"} {
		t.Setenv(variable, filepath.Join(hostile, ".git"))
	}
	req.Operation, req.Path, req.MemberID = "status", "member", "one"
	res := e.Explore(t.Context(), req)
	if !res.OK || res.Status == nil || res.Status.Untracked != 1 {
		t.Fatalf("status read hostile sibling configuration: %+v", res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("hostile sibling hook ran")
	}
	snapshot, err := e.statusSnapshot(t.Context(), "member", hostWorktreePin{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	if _, err := os.Stat(filepath.Join(snapshot.directory, "worktrees")); !os.IsNotExist(err) {
		t.Fatal("sibling records reached the private Git directory")
	}
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := pinGitDir(repo, info)
	if !ok {
		t.Fatal("could not pin fixture")
	}
	defer p.close()
	options := snapshot.options(p, nil)
	environment := hostgit.Environ(options, "/usr/bin/git")
	worktree := "/proc/self/fd/3"
	if runtime.GOOS == "darwin" {
		worktree = "."
	}
	for _, want := range []string{"GIT_DIR=" + snapshot.directory, "GIT_COMMON_DIR=" + snapshot.directory, "GIT_WORK_TREE=" + worktree, "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_CONFIG_GLOBAL=/dev/null"} {
		if !slices.Contains(environment, want) {
			t.Errorf("missing fenced environment %q", want)
		}
	}
	env := strings.Join(environment, "\n")
	if strings.Contains(env, hostile) {
		t.Fatal("hostile inherited Git environment reached status")
	}
}

func TestWorkspaceStatusPointerRefusals(t *testing.T) {
	for _, name := range []string{
		"commondir", "gitdir", "objects/info/alternates", "objects/info/http-alternates",
		"worktrees/alternates", "worktrees/sibling/alternates", "worktrees/sibling/http-alternates",
		"worktrees/sibling/nested/gitdir", "nested/worktrees/sibling/commondir",
	} {
		t.Run(name, func(t *testing.T) {
			e, root, req := explorerFixture(t)
			repo := filepath.Join(root, "member")
			initChildRepo(t, repo)
			e.pinMembers()
			writeStatusFixture(t, filepath.Join(repo, ".git", name), "/unapproved/metadata\n")
			req.Operation, req.Path, req.MemberID = "status", "member", "one"
			if res := e.Explore(t.Context(), req); res.OK || res.StatusReason != "metadata_pointer" {
				t.Fatalf("pointer %s accepted or unclear refusal: %+v", name, res)
			}
		})
	}
}

func TestWorkspaceStatusSiblingRecordChangeDuringGit(t *testing.T) {
	e, root, req := explorerFixture(t)
	repo := filepath.Join(root, "member")
	initChildRepo(t, repo)
	childRepoGit(t, repo, "worktree", "add", "-q", "--detach", filepath.Join(root, "sibling"))
	e.pinMembers()
	changed := false
	ctx := context.WithValue(t.Context(), workspaceStatusTimingKey{}, workspaceStatusTiming(func(stage string, _, _ time.Duration, _ error) {
		if stage == "Git status" {
			writeStatusFixture(t, filepath.Join(repo, ".git", "worktrees", "sibling", "commondir"), "/changed/metadata\n")
			changed = true
		}
	}))
	req.Operation, req.Path, req.MemberID = "status", "member", "one"
	if res := e.Explore(ctx, req); !changed || res.OK || res.Status != nil {
		t.Fatalf("changed record delivered status: changed=%v result=%+v", changed, res)
	}
}

func TestWorkspaceStatusUnapprovedLinkedRoots(t *testing.T) {
	for _, target := range []string{"unregistered-gitdir", "registered-outside-root", "prefix-outside-root", "commondir-outside-root"} {
		t.Run(target, func(t *testing.T) {
			e, root, req := explorerFixture(t)
			source := filepath.Join(root, "source")
			if target != "commondir-outside-root" {
				source = t.TempDir()
			}
			if target == "prefix-outside-root" {
				source = root + "-outside"
				t.Cleanup(func() { _ = os.RemoveAll(source) })
			}
			initChildRepo(t, source)
			member := filepath.Join(root, "member")
			childRepoGit(t, source, "worktree", "add", "-q", "--detach", member)
			switch target {
			case "unregistered-gitdir":
				writeStatusFixture(t, filepath.Join(member, ".git"), "gitdir: "+filepath.Join(source, ".git")+"\n")
			case "commondir-outside-root":
				writeStatusFixture(t, filepath.Join(source, ".git", "worktrees", "member", "commondir"), t.TempDir()+"\n")
			}
			e.pinMembers()
			req.Operation, req.Path, req.MemberID = "status", "member", "one"
			if res := e.Explore(t.Context(), req); res.OK || res.StatusReason != "metadata_pointer" {
				t.Fatalf("unapproved linked roots accepted or unclear refusal: %+v", res)
			}
		})
	}
}

func TestWorkspaceStatusSiblingRecordsRemainBounded(t *testing.T) {
	for _, pointer := range []string{"gitdir", "commondir"} {
		for _, kind := range []string{"directory", "symlink", "hardlink", "oversized"} {
			t.Run(pointer+"/"+kind, func(t *testing.T) {
				e, root, req := explorerFixture(t)
				repo := filepath.Join(root, "member")
				initChildRepo(t, repo)
				e.pinMembers()
				name := filepath.Join(repo, ".git", "worktrees", "sibling", pointer)
				writeStatusFixture(t, name, "../../\n")
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "directory":
					if err := os.Mkdir(name, 0700); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Symlink(filepath.Join(repo, ".git", "HEAD"), name); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(filepath.Join(repo, ".git", "HEAD"), name); err != nil {
						t.Fatal(err)
					}
				case "oversized":
					writeStatusFixture(t, name, strings.Repeat("x", 4097))
				}
				req.Operation, req.Path, req.MemberID = "status", "member", "one"
				if res := e.Explore(t.Context(), req); res.OK || res.Status != nil {
					t.Fatalf("nonregular or unbounded sibling record accepted: %+v", res)
				}
			})
		}
	}
	// Accepted sibling records still consume the shared entry/byte budget.
	for _, limit := range []string{"entries", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			dir := t.TempDir()
			writeStatusFixture(t, filepath.Join(dir, "worktrees", "sibling", "gitdir"), "inert\n")
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			budget := &metadataBudget{entries: 200000}
			if limit == "entries" {
				budget.entries = 2
			} else {
				budget.bytes = 256 << 20
			}
			if _, safe := metadataTree(t.Context(), root, ".", nil, true, budget); safe || budget.reason != "metadata_limit" {
				t.Fatalf("sibling records escaped %s budget: %+v", limit, budget)
			}
		})
	}
}

func TestWorkspaceStatusLinkedSiblingFingerprint(t *testing.T) {
	e, root, req := explorerFixture(t)
	source := filepath.Join(root, "source")
	initChildRepo(t, source)
	childRepoGit(t, source, "worktree", "add", "-q", "-b", "linked", filepath.Join(root, "member"))
	childRepoGit(t, source, "worktree", "add", "-q", "--detach", filepath.Join(root, "sibling"))
	e.pinMembers()
	pin, ok := e.worktrees["one"]
	if !ok {
		t.Fatal("in-root linked metadata was not pinned")
	}
	req.Operation, req.Path, req.MemberID = "status", "member", "one"
	if res := e.Explore(t.Context(), req); !res.OK || res.Status == nil || res.Status.Branch != "linked" {
		t.Fatalf("in-root linked status: %+v", res)
	}
	before, safe := pin.check(t.Context())
	if !safe {
		t.Fatal("in-root linked metadata refused")
	}
	writeStatusFixture(t, filepath.Join(source, ".git", "worktrees", "sibling", "gitdir"), "/changed/sibling/.git\n")
	after, safe := pin.check(t.Context())
	if !safe || before == after {
		t.Fatal("linked status did not fingerprint sibling records in common metadata")
	}
	// The active checkout's own pointers retain exact launch-time validation.
	writeStatusFixture(t, filepath.Join(source, ".git", "worktrees", "member", "commondir"), "/changed/common\n")
	if _, safe := pin.check(t.Context()); safe {
		t.Fatal("linked checkout accepted a changed active pointer")
	}
}

func TestWorkspaceStatusSiblingPointerDirectoryNames(t *testing.T) {
	e, root, req := explorerFixture(t)
	repo := filepath.Join(root, "member")
	initChildRepo(t, repo)
	for _, name := range []string{"gitdir", "commondir", "alternates", "http-alternates"} {
		childRepoGit(t, repo, "worktree", "add", "-q", "--detach", filepath.Join(root, name))
	}
	e.pinMembers()
	req.Operation, req.Path, req.MemberID = "status", "member", "one"
	if res := e.Explore(t.Context(), req); !res.OK || res.Status == nil || res.Status.Branch != "main" {
		t.Fatalf("valid worktree names refused: %+v", res)
	}
}
