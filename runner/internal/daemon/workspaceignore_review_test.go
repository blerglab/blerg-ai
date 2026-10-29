package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceSearchManyDirectories(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	for dir := 0; dir < 2000; dir++ {
		for file := 0; file < 5; file++ {
			name := fmt.Sprintf("dir-%04d/file-%d.js", dir, file)
			if dir == 0 && file == 0 {
				name = "dir-0000/match-real.js"
			}
			ignoreFile(t, root, name, "")
		}
	}
	synctest.Test(t, func(t *testing.T) {
		// Filesystem work must obey the visit budget, independently of host
		// scheduling or race-detector overhead. Virtual time advances only
		// when the search blocks, not while it walks the real fixture.
		budget := protocol.WorkspaceSearchVisited
		res := e.searchWithBudget(t.Context(), req, req.Path, &budget, protocol.WorkspaceSearchHits, e.entryReplyBudget(req), 0)
		if !res.OK || !res.Truncated || len(res.Entries) != 1 || res.Entries[0].Path != "member/dir-0000/match-real.js" || budget != 0 {
			t.Fatalf("large search did not honor its visit budget (%d left): %+v", budget, res)
		}
	})

	// Exercise Explore's own two-second deadline at its delivery check on a
	// small scope that does not exhaust the visit budget. This also catches
	// removal of the deadline, independently of the large-tree budget above.
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	req.Path = "member/dir-0000"
	for _, delay := range []time.Duration{2*time.Second - time.Nanosecond, 2 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				e.live = func() bool {
					calls++
					if calls == 2 {
						// Let the deadline callback run too when both timers fire together.
						time.Sleep(delay)
						synctest.Wait()
					}
					return true
				}
				res := e.Explore(t.Context(), req)
				if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/dir-0000/match-real.js" || res.Truncated != (delay == 2*time.Second) {
					t.Fatalf("search at deadline boundary %v: %+v", delay, res)
				}
			})
		})
	}
}

func TestWorkspaceSearchUntrustedHome(t *testing.T) {
	for _, runtime := range []string{"sandbox", "cluster"} {
		t.Run(runtime, func(t *testing.T) {
			e, root, req := ignoreRepoFixture(t, runtime)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			ignoreFile(t, home, ".gitconfig", "[core]\n excludesFile = ~/private-file\n")
			ignoreFile(t, home, "private-file", "match-real.js\n")
			ignoreFile(t, root, "match-real.js", "")
			res := e.Explore(t.Context(), req)
			if !res.OK || len(res.Entries) != 1 {
				t.Fatalf("consulted agent configuration: %+v", res)
			}
		})
	}
}

func TestWorkspaceSearchNestedRepositories(t *testing.T) {
	for _, mode := range []string{"member", "discovered", "non-git-outer"} {
		t.Run(mode, func(t *testing.T) {
			e, root, req := ignoreRepoFixture(t, "host")
			outer := filepath.Dir(root)
			if mode != "non-git-outer" {
				childRepoGit(t, outer, "init")
				ignoreFile(t, outer, ".gitignore", "member/\n")
			}
			ignoreFile(t, root, ".gitignore", "dist/\n")
			ignoreFile(t, root, "src/match-real.js", "")
			childRepoGit(t, root, "add", "src/match-real.js")
			ignoreFile(t, root, "dist/match-built.js", "")
			if mode != "member" {
				e.manifest.Members = nil
			}
			e.manifest.Members = append(e.manifest.Members, protocol.WorkspaceMember{ID: "outer", Name: "outer", Path: "."})
			e.pinMembers()
			req.Path = ""
			res := e.Explore(t.Context(), req)
			if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/src/match-real.js" {
				t.Fatalf("nested repository: %+v", res)
			}
		})
	}
}

func TestWorkspaceSearchColonNames(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	ignoreFile(t, root, ".gitignore", "dist/\n")
	for _, name := range []string{":(bogus)x", ":!x", "dist/match-built.js", "src/match-real.js"} {
		ignoreFile(t, root, name, "")
	}
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/src/match-real.js" {
		t.Fatalf("colon names disabled rules: %+v", res)
	}
}

func TestWorkspaceListDoesNotRunGit(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	ignoreFile(t, root, ".gitignore", "dist/\n")
	ignoreFile(t, root, "dist/match-built.js", "")
	marker := filepath.Join(t.TempDir(), "called")
	binary := filepath.Join(t.TempDir(), "git")
	ignoreFile(t, filepath.Dir(binary), "git", "#!/bin/sh\nprintf called > '"+marker+"'\nexit 1\n")
	if err := os.Chmod(binary, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hostgit.SetBinaryForTest(binary))
	req.Operation = "list"
	res := e.Explore(t.Context(), req)
	if !res.OK {
		t.Fatal(res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("list invoked git")
	}
}

func TestWorkspaceSearchSharedPreparationBudget(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("repo-%d", i)
		sub := filepath.Join(root, name)
		if err := os.Mkdir(sub, 0700); err != nil {
			t.Fatal(err)
		}
		childRepoGit(t, sub, "init")
		ignoreFile(t, sub, "match-real.js", "")
		e.manifest.Members = append(e.manifest.Members, protocol.WorkspaceMember{ID: name, Name: name, Path: "member/" + name})
	}
	e.pinMembers()
	binary := filepath.Join(t.TempDir(), "git")
	ignoreFile(t, filepath.Dir(binary), "git", "#!/bin/sh\nsleep 10\n")
	if err := os.Chmod(binary, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hostgit.SetBinaryForTest(binary))
	start := time.Now()
	ctx := context.WithValue(t.Context(), workspaceIgnoreBudgetKey{}, &workspaceIgnoreBudget{remaining: 400 * time.Millisecond})
	res := e.Explore(ctx, req)
	if elapsed := time.Since(start); !res.OK || len(res.Entries) != 3 || elapsed > 900*time.Millisecond {
		t.Fatalf("preparation budget %v: %+v", elapsed, res)
	}
}

func TestWorkspaceSearchGlobalConfigOverride(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ignoreFile(t, home, "alternate", "[core]\n excludesFile = ~/rules\n")
	ignoreFile(t, home, "rules", "match-global.js\n")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "alternate"))
	ignoreFile(t, root, "match-global.js", "")
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 0 {
		t.Fatal(res)
	}
}

func TestWorkspaceSearchUserExcludesNoFollow(t *testing.T) {
	e, root, _ := ignoreRepoFixture(t, "host")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ignoreFile(t, home, ".gitconfig", "[core]\n excludesFile = linked-rules\n")
	ignoreFile(t, home, "rules", "match.js\n")
	if err := os.Symlink(filepath.Join(home, "rules"), filepath.Join(root, "linked-rules")); err != nil {
		t.Fatal(err)
	}
	file, err := e.openDirectory("member")
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := pinGitDir(root, info)
	if !ok {
		t.Fatal("pin")
	}
	defer pin.close()
	budget := &metadataBudget{entries: 4, reserve: func(int64, int) error { return nil }}
	if err := copyWorkspaceUserExcludes(t.Context(), t.TempDir(), budget, pin); err == nil {
		t.Fatal("followed excludes symlink")
	}
}

func TestWorkspaceSearchDeadlinePartial(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	ignoreFile(t, root, "match-real.js", "")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	calls := 0
	e.live = func() bool {
		calls++
		if calls == 2 {
			<-ctx.Done()
		}
		return true
	}
	res := e.Explore(ctx, req)
	if !res.OK || !res.Truncated || len(res.Entries) != 1 {
		t.Fatalf("deadline discarded results: %+v", res)
	}
	budget := 100
	res = e.searchWithBudget(ctx, req, req.Path, &budget, 200, 1<<20, 0)
	if !res.OK || !res.Truncated {
		t.Fatalf("deadline during walk: %+v", res)
	}
}

// Mutate after the directory is opened, at the first cancellation check.
type ignoreMutationContext struct {
	context.Context
	mutate func()
}

func (c *ignoreMutationContext) Err() error {
	if c.mutate != nil {
		mutate := c.mutate
		c.mutate = nil
		mutate()
	}
	return c.Context.Err()
}
func TestWorkspaceSearchTrackedDirectoryChecks(t *testing.T) {
	for _, mode := range []string{"unavailable", "stale", "badges"} {
		t.Run(mode, func(t *testing.T) {
			e, root, _ := ignoreRepoFixture(t, "host")
			ignoreFile(t, root, "ignored/match-real.js", "")
			ignoreFile(t, root, "ignored/nested/match-child.js", "")
			childRepoGit(t, filepath.Join(root, "ignored/nested"), "init")
			rules := &workspaceIgnoreRules{root: "member", children: map[string][]string{"ignored": {"ignored/match-real.js", "ignored/nested"}}}
			if mode == "badges" {
				e.manifest.Members = append(e.manifest.Members, protocol.WorkspaceMember{ID: "nested", Name: "nested", Path: "member/ignored/nested"})
				e.pinMembers()
			}
			ctx := &ignoreMutationContext{Context: t.Context()}
			if mode == "unavailable" {
				e.memberRoots["one"] = protocol.WorkspaceRoot{}
			}
			if mode == "stale" {
				ctx.mutate = func() {
					directory := filepath.Join(root, "ignored")
					before, err := os.Stat(directory)
					if err != nil {
						t.Fatal(err)
					}
					ignoreFile(t, root, "ignored/new-file.js", "")
					// Directory additions can share a filesystem timestamp tick. Force a
					// distinct fingerprint rather than relying on wall-clock scheduling.
					changed := before.ModTime().Add(time.Second)
					if err := os.Chtimes(directory, changed, changed); err != nil {
						t.Fatal(err)
					}
				}
			}
			budget := 100
			rows, _, err := e.readTrackedDirectory(ctx, "member/ignored", &budget, rules)
			if mode == "badges" {
				if err != nil || len(rows) != 2 || !rows[1].Repository || rows[1].MemberID != "nested" {
					t.Fatalf("tracked badges: %v %v", rows, err)
				}
			} else if err == nil {
				t.Fatalf("tracked walk omitted %s check: %v", mode, rows)
			}
		})
	}
}

func TestWorkspaceSearchLightweightSnapshot(t *testing.T) {
	e, root, _ := ignoreRepoFixture(t, "host")
	ignoreFile(t, root, ".gitignore", "dist/\n")
	ignoreFile(t, root, "dist/match-built.js", "")
	// Neither ignore command needs to inspect repository history, even when that
	// history contains metadata the status snapshot must reject.
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".git/objects/foreign")); err != nil {
		t.Fatal(err)
	}
	rules := &workspaceIgnoreRules{root: "member", failed: true}
	defer func() {
		if rules.snapshot != nil {
			rules.snapshot.close()
		}
		if rules.pin != nil {
			rules.pin.close()
		}
	}()
	if err := rules.prepare(t.Context(), e, "one"); err != nil {
		t.Fatal(err)
	}
	if rules.failed || rules.snapshot.objects != nil {
		t.Fatal("ignore preparation used the object store")
	}
	for _, directory := range []string{"refs", "objects"} {
		entries, err := os.ReadDir(filepath.Join(rules.snapshot.directory, directory))
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s was copied: %v %v", directory, entries, err)
		}
	}
}

func TestWorkspaceSearchXDGUserExcludes(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ignoreFile(t, home, ".config/git/ignore", "match-global.js\n")
	ignoreFile(t, root, "match-global.js", "")
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 0 {
		t.Fatal(res)
	}
}

func TestWorkspaceSearchVirtualSharedPreparationBudget(t *testing.T) {
	namespace, _, req := explorerFixture(t)
	virtual := &VirtualHostExplorer{namespace: namespace, members: map[string]*LocalExplorer{}, manifest: protocol.WorkspaceManifest{LaunchGeneration: "launch"}}
	for i := 0; i < 3; i++ {
		local, root, _ := ignoreRepoFixture(t, "host")
		// Virtual members expose their source repository at the local root.
		local.manifest.Members = []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "member"}}
		ignoreFile(t, root, "match-real.js", "")
		name := fmt.Sprintf("repo-%d", i)
		virtual.manifest.Members = append(virtual.manifest.Members, protocol.WorkspaceMember{ID: name, Name: name, Path: name})
		virtual.members[name] = local
	}
	binary := filepath.Join(t.TempDir(), "git")
	ignoreFile(t, filepath.Dir(binary), "git", "#!/bin/sh\nsleep 10\n")
	if err := os.Chmod(binary, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hostgit.SetBinaryForTest(binary))
	req.Operation, req.Filter = "search", "match"
	start := time.Now()
	ctx := context.WithValue(t.Context(), workspaceIgnoreBudgetKey{}, &workspaceIgnoreBudget{remaining: 400 * time.Millisecond})
	res := virtual.Explore(ctx, req)
	if elapsed := time.Since(start); !res.OK || len(res.Entries) != 3 || elapsed > 900*time.Millisecond {
		t.Fatalf("virtual preparation budget %v: %+v", elapsed, res)
	}
}
