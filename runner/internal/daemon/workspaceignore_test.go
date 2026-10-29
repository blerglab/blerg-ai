package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func ignoreRepoFixture(t *testing.T, runtime string) (*LocalExplorer, string, protocol.WorkspaceExplorerRequest) {
	t.Helper()
	e, root, req := explorerFixture(t)
	member := filepath.Join(root, "member")
	if err := os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	childRepoGit(t, member, "init")
	e.manifest.Runtime = runtime
	e.pinMembers()
	req.Path = "member"
	req.Operation = "search"
	req.Filter = "match"
	return e, member, req
}
func ignoreFile(t *testing.T, root, name, content string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestWorkspaceSearchGitIgnores(t *testing.T) {
	for _, runtime := range []string{"host", "sandbox", "cluster"} {
		t.Run(runtime, func(t *testing.T) {
			e, root, req := ignoreRepoFixture(t, runtime)
			ignoreFile(t, root, ".gitignore", "node_modules/\nignored/\n")
			for i := 0; i < 3000; i++ {
				ignoreFile(t, root, fmt.Sprintf("node_modules/match-%04d.js", i), "")
			}
			ignoreFile(t, root, "src/deep/match-real.js", "")
			ignoreFile(t, root, "src/.gitignore", "/match-*.js\n!match-keep.js\n")
			ignoreFile(t, root, "src/match-keep.js", "")
			ignoreFile(t, root, "src/match-drop.js", "")
			ignoreFile(t, root, ".git/info/exclude", "match-excluded.js\n")
			ignoreFile(t, root, "match-excluded.js", "")
			ignoreFile(t, root, "ignored/match-tracked.js", "")
			childRepoGit(t, root, "add", "-f", "ignored/match-tracked.js")
			ignoreFile(t, root, "ignored/match-untracked.js", "")
			budget := protocol.WorkspaceSearchVisited
			res := e.searchWithBudget(t.Context(), req, req.Path, &budget, protocol.WorkspaceSearchHits, protocol.WorkspaceExplorerReplyBytes, 0)
			if !res.OK || res.Truncated {
				t.Fatalf("search failed: ok=%v truncated=%v hits=%d", res.OK, res.Truncated, len(res.Entries))
			}
			want := map[string]bool{"member/src/deep/match-real.js": true, "member/src/match-keep.js": true, "member/ignored/match-tracked.js": true}
			for _, row := range res.Entries {
				if !want[row.Path] {
					t.Errorf("unexpected match: %s", row.Path)
				}
				delete(want, row.Path)
			}
			if len(want) > 0 {
				t.Errorf("missing matches: %v", want)
			}
			if visited := protocol.WorkspaceSearchVisited - budget; visited > 20 {
				t.Errorf("ignored directory was visited: %d entries", visited)
			}
			req.Operation = "list"
			req.Filter = ""
			res = e.Explore(t.Context(), req)
			if !res.OK {
				t.Fatalf("list: %+v", res)
			}
			// Listing stays browsable without preparing or reporting ignore state.
			found := false
			for _, row := range res.Entries {
				if row.Name == "node_modules" {
					found = true
				}
			}
			if !found {
				t.Error("ignored directory disappeared from browsing")
			}
			req.Path = "member/node_modules"
			res = e.Explore(t.Context(), req)
			if !res.OK || len(res.Entries) == 0 {
				t.Fatalf("ignored directory cannot be browsed: %+v", res)
			}
		})
	}
}
func TestWorkspaceSearchFallbackIgnores(t *testing.T) {
	for _, mode := range []string{"non-git", "missing", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			e, root, req := ignoreRepoFixture(t, "host")
			switch mode {
			case "non-git":
				if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
					t.Fatal(err)
				}
			case "missing":
				t.Cleanup(hostgit.SetBinaryForTest(filepath.Join(root, "absent-git")))
			case "timeout":
				binary := filepath.Join(root, "slow-git")
				if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(hostgit.SetBinaryForTest(binary))
			}
			for _, name := range []string{"node_modules", ".git", ".hg", ".svn", ".venv", "venv", "__pycache__", ".mypy_cache", ".pytest_cache", ".cache", ".next", ".nuxt", ".turbo", ".gradle", "target", "bower_components"} {
				ignoreFile(t, root, name+"/match-hidden.txt", "")
			}
			ignoreFile(t, root, "src/deep/match-real.txt", "")
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			res := e.Explore(ctx, req)
			if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/src/deep/match-real.txt" {
				t.Fatalf("fallback search: %+v", res)
			}
		})
	}
}

func TestWorkspaceSearchUserExcludes(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ignoreFile(t, home, ".gitconfig", "[include]\n path = settings\n")
	ignoreFile(t, home, "settings", "[core]\n excludesFile = ~/ignore-rules\n")
	ignoreFile(t, home, "ignore-rules", "match-global.js\n")
	// Ambient Git configuration must not inject a different ignore source.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.excludesFile")
	t.Setenv("GIT_CONFIG_VALUE_0", "/dev/null")
	ignoreFile(t, root, "match-global.js", "")
	ignoreFile(t, root, "src/match-real.js", "")
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/src/match-real.js" {
		t.Fatalf("global ignores: %+v", res)
	}
}

func TestWorkspaceSearchTrackedIgnoredBudget(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	ignoreFile(t, root, ".gitignore", "node_modules/\n")
	for i := 0; i < 1200; i++ {
		ignoreFile(t, root, fmt.Sprintf("node_modules/match-%04d.js", i), "")
	}
	ignoreFile(t, root, "node_modules/deep/match-tracked.js", "")
	childRepoGit(t, root, "add", "-f", "node_modules/deep/match-tracked.js")
	budget := 20
	res := e.searchWithBudget(t.Context(), req, req.Path, &budget, 200, 1<<20, 0)
	if !res.OK || res.Truncated || len(res.Entries) != 1 || res.Entries[0].Path != "member/node_modules/deep/match-tracked.js" {
		t.Fatalf("tracked file lost behind ignored siblings: ok=%v truncated=%v hits=%v budget=%v", res.OK, res.Truncated, res.Entries, budget)
	}
}

func TestWorkspaceSearchIgnoreLimits(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	for i := 0; i < 250; i++ {
		ignoreFile(t, root, fmt.Sprintf("match-%03d.js", i), "")
	}
	for _, test := range []struct {
		name                string
		budget, hits, bytes int
	}{
		{"visited", 10, 200, 1 << 20}, {"hits", 10000, 7, 1 << 20}, {"bytes", 10000, 200, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := test.budget
			res := e.searchWithBudget(t.Context(), req, req.Path, &budget, test.hits, test.bytes, 0)
			if !res.OK || !res.Truncated || len(res.Entries) > test.hits || budget < 0 {
				t.Fatalf("limits: %+v budget=%d", res, budget)
			}
			if test.name == "visited" && budget != 0 {
				t.Fatal("visited budget changed")
			}
			if test.name == "hits" && len(res.Entries) != 7 {
				t.Fatal("hit limit changed")
			}
			if test.name == "bytes" && len(res.Entries) != 0 {
				t.Fatal("byte limit changed")
			}
		})
	}
}

func TestWorkspaceSearchScopedIgnoredDirectory(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	ignoreFile(t, root, ".gitignore", "ignored/\n")
	ignoreFile(t, root, "ignored/match-untracked.js", "")
	req.Path = "member/ignored"
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 1 {
		t.Fatalf("scoped ignored search: %+v", res)
	}
	ignoreFile(t, root, "ignored/match-tracked.js", "")
	childRepoGit(t, root, "add", "-f", "ignored/match-tracked.js")
	res = e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 2 {
		t.Fatalf("scoped tracked search: %+v", res)
	}
}

func TestWorkspaceSearchGitDoesNotApplyFallbackList(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	ignoreFile(t, root, "target/match-source.rs", "")
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/target/match-source.rs" {
		t.Fatalf("unignored directory was pruned: %+v", res)
	}
}

func TestWorkspaceSearchHelperProtocolIgnores(t *testing.T) {
	root := t.TempDir()
	childRepoGit(t, root, "init")
	ignoreFile(t, root, ".gitignore", "node_modules/\n")
	ignoreFile(t, root, "node_modules/match-hidden.js", "")
	ignoreFile(t, root, "src/match-real.js", "")
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	reader, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer output.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeWorkspaceExplorer(ctx, root, input, output) }()
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "owner", Runtime: "sandbox", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "repository", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/repo"}}}
	start := WorkspaceExplorerStart{SessionID: "session", Manifest: manifest, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}}
	encoder, decoder := json.NewEncoder(writer), json.NewDecoder(reader)
	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(start); err != nil {
		t.Fatal(err)
	}
	var prepared helperPrepared
	if err := decoder.Decode(&prepared); err != nil {
		t.Fatal(err)
	}
	req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "find", SessionID: "session", WorkspaceRuntimeRef: start.Runtime, Operation: "search", Filter: "match"}
	if err := encoder.Encode(req); err != nil {
		t.Fatal(err)
	}
	var result protocol.WorkspaceExplorerResult
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Entries) != 1 || result.Entries[0].Path != "src/match-real.js" {
		t.Fatalf("helper search: %+v", result)
	}
	_ = writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper cleanup failed")
	}
}

func TestWorkspaceSearchConditionalUserExcludes(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ignoreFile(t, home, ".gitconfig", fmt.Sprintf("[includeIf \"gitdir:%s/.git\"]\n path = settings\n", filepath.ToSlash(root)))
	ignoreFile(t, home, "settings", "[core]\n excludesFile = global.rules\n")
	ignoreFile(t, root, "global.rules", "match-global.js\n")
	ignoreFile(t, root, "match-global.js", "")
	ignoreFile(t, root, "src/match-real.js", "")
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/src/match-real.js" {
		t.Fatalf("conditional global ignores: %+v", res)
	}
}

func TestWorkspaceSearchIgnoreAllRoot(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	ignoreFile(t, root, ".gitignore", "*\n!src/\n!src/*.js\n")
	ignoreFile(t, root, "match-tracked.js", "")
	childRepoGit(t, root, "add", "-f", "match-tracked.js")
	ignoreFile(t, root, "match-hidden.js", "")
	ignoreFile(t, root, "src/match-real.js", "")
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 2 || res.Entries[0].Path != "member/match-tracked.js" || res.Entries[1].Path != "member/src/match-real.js" {
		t.Fatalf("ignore-all repository root hid allowed files: %+v", res)
	}
}
