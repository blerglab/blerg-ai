package gitremote

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

func TestParseGitHub(t *testing.T) {
	good := map[string]string{
		"https://github.com/example-org/example-repo":           "example-org/example-repo",
		"https://github.com/example-org/example-repo.git":       "example-org/example-repo",
		"https://github.com/example-org/example-repo/":          "example-org/example-repo",
		"https://github.com/example-org/example-repo.git/":      "example-org/example-repo",
		"https://GitHub.com/example-org/example-repo.git":       "example-org/example-repo",
		"http://github.com/example-org/example-repo":            "example-org/example-repo",
		"https://user:token@github.com/example-org/example.app": "example-org/example.app",
		"git@github.com:example-org/example-repo.git":           "example-org/example-repo",
		"git@github.com:example-org/example-repo":               "example-org/example-repo",
		"git@github.com:example-org/example-repo/":              "example-org/example-repo",
		"github.com:example-org/example_repo":                   "example-org/example_repo",
		"ssh://git@github.com/example-org/example-repo.git":     "example-org/example-repo",
		"ssh://git@github.com/example-org/example-repo":         "example-org/example-repo",
		"  git@github.com:Example-Org/Repo-1.git  ":             "Example-Org/Repo-1",
	}
	for in, want := range good {
		got, ok := ParseGitHub(in)
		if !ok || got != want {
			t.Errorf("ParseGitHub(%q) = %q,%v; want %q,true", in, got, ok, want)
		}
	}

	bad := []string{
		"",
		"   ",
		"example-repo",
		"/home/dev/example-repo",
		"../example-repo",
		"file:///srv/git/example-org/example-repo.git",
		"https://gitlab.com/example-org/example-repo.git",
		"git@gitlab.com:example-org/example-repo.git",
		"https://git.example.test/example-org/example-repo.git",
		"https://github.example.test/example-org/example-repo.git",
		"https://github.com.evil.example/example-org/example-repo.git",
		"https://github.com@evil.example/example-org/example-repo.git",
		"git@github.com@evil.example:example-org/example-repo.git",
		"https://github.com:8443/example-org/example-repo.git",
		"ssh://git@github.com:22/example-org/example-repo.git",
		"ssh://git@ssh.github.com:443/example-org/example-repo.git",
		"git://github.com/example-org/example-repo.git",
		"ftp://github.com/example-org/example-repo.git",
		"https://github.com/example-org",
		"https://github.com/example-org/",
		"https://github.com/example-org/example-repo/tree/main",
		"https://github.com/example-org/example-repo/extra",
		"https://github.com//example-org/example-repo",
		"https://github.com/example-org//example-repo",
		"git@github.com:/example-org/example-repo.git",
		"git@github.com:example-org/example-repo/extra.git",
		"git@github.com:example-org",
		"https://github.com/example-org/example-repo?ref=main",
		"https://github.com/example-org/example-repo#frag",
		"https://github.com/example-org/%2e%2e",
		"https://github.com/example-org/..",
		"https://github.com/example-org/.hidden",
		"https://github.com/../example-repo",
		"https://github.com/-bad/example-repo",
		"https://github.com/bad_org/example-repo",
		"https://github.com/example-org/exa mple",
		"https://github.com/example-org/example\nrepo",
		"https://github.com/example-org/exa\x00mple",
		"https://github.com/example-org/exa\\mple",
		"https://github.com/example-org/" + strings.Repeat("a", 101),
		"https://github.com/" + strings.Repeat("o", 40) + "/example-repo",
		"https://github.com/example-org/ex$ample",
		"https://github.com/example-org/ex;rm -rf",
		"github.com/example-org/example-repo",
	}
	for _, in := range bad {
		if got, ok := ParseGitHub(in); ok {
			t.Errorf("ParseGitHub(%q) = %q,true; want rejected", in, got)
		}
	}
}

func TestValidOrgName(t *testing.T) {
	for _, s := range []string{"example-org/example-repo", "a/b", "Org1/repo.name_x-y"} {
		if !ValidOrgName(s) {
			t.Errorf("ValidOrgName(%q) = false", s)
		}
	}
	for _, s := range []string{"", "a", "a/", "/b", "a/b/c", "a/.b", "../b", "a/..", "-a/b", "a_b/c", "a/b c"} {
		if ValidOrgName(s) {
			t.Errorf("ValidOrgName(%q) = true", s)
		}
	}
}

func TestRemoteURLsFromConfig(t *testing.T) {
	cases := map[string]map[string]string{
		"[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:example-org/example-repo.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n": {"origin": "git@github.com:example-org/example-repo.git"},
		"[remote \"upstream\"]\n\turl = https://github.com/other/x\n[remote \"origin\"]\n\tURL = \"https://github.com/example-org/example-repo\"\n":         {"upstream": "https://github.com/other/x", "origin": "https://github.com/example-org/example-repo"},
		"[REMOTE \"origin\"]\nurl=https://github.com/example-org/example-repo ; trailing comment\n":                                                         {"origin": "https://github.com/example-org/example-repo"},
		"[remote \"Origin\"]\n\turl = https://github.com/example-org/example-repo\n":                                                                        {"Origin": "https://github.com/example-org/example-repo"},
		// A repo with no "origin" at all, set up under other remote names —
		// e.g. a personal push remote plus a "github" remote.
		"[remote \"pushremote\"]\n\turl = deploy@203.0.113.7:team/project.git\n[remote \"github\"]\n\turl = git@github.com:example-org/example-repo.git\n": {"pushremote": "deploy@203.0.113.7:team/project.git", "github": "git@github.com:example-org/example-repo.git"},
		"# [remote \"origin\"]\n# url = https://github.com/example-org/example-repo\n":                                                                     {},
		"":                                      {},
		"garbage\x00\xff[[[\n= = =\n":           {},
		"[remote \"origin\"\nurl = https://x\n": {},
		// Two url= lines under one remote: git (and this reader) keeps the first.
		"[remote \"origin\"]\n\turl = https://github.com/example-org/first\n\turl = https://github.com/example-org/second\n": {"origin": "https://github.com/example-org/first"},
	}
	for in, want := range cases {
		got := remoteURLsFromConfig([]byte(in))
		if len(got) != len(want) {
			t.Errorf("remoteURLsFromConfig(%q) = %v, want %v", in, got, want)
			continue
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("remoteURLsFromConfig(%q)[%q] = %q, want %q", in, k, got[k], v)
			}
		}
	}
}

func TestOriginURLFallsBackWhenNoOrigin(t *testing.T) {
	root := t.TempDir()
	// The motivating shape: no "origin", a non-GitHub push remote and
	// one GitHub remote — the single GitHub remote wins.
	writeRepo(t, root, "one-github-remote",
		"[remote \"pushremote\"]\n\turl = deploy@203.0.113.7:team/project.git\n"+
			"[remote \"github\"]\n\turl = git@github.com:example-org/example-repo.git\n")
	if got := OriginURL(filepath.Join(root, "one-github-remote")); got != "git@github.com:example-org/example-repo.git" {
		t.Errorf("OriginURL = %q, want the sole github remote", got)
	}

	// Two DIFFERENT GitHub remotes: ambiguous, stays unresolved rather than
	// guessed (they may be a fork and its upstream, pointing at different repos).
	writeRepo(t, root, "two-github-remotes",
		"[remote \"upstream\"]\n\turl = https://github.com/other-org/other-repo\n"+
			"[remote \"mine\"]\n\turl = https://github.com/example-org/example-repo\n")
	if got := OriginURL(filepath.Join(root, "two-github-remotes")); got != "" {
		t.Errorf("OriginURL = %q, want unresolved with two different github remotes", got)
	}

	// No remote at all resolves to a GitHub URL.
	writeRepo(t, root, "no-github-remote",
		"[remote \"pushremote\"]\n\turl = deploy@203.0.113.7:team/project.git\n")
	if got := OriginURL(filepath.Join(root, "no-github-remote")); got != "" {
		t.Errorf("OriginURL = %q, want unresolved with no github remote", got)
	}

	// "origin" present always wins over any fallback, even when another
	// remote also parses as GitHub.
	writeRepo(t, root, "origin-wins",
		"[remote \"github\"]\n\turl = https://github.com/other-org/other-repo\n"+
			"[remote \"origin\"]\n\turl = https://github.com/example-org/example-repo\n")
	if got := OriginURL(filepath.Join(root, "origin-wins")); got != "https://github.com/example-org/example-repo" {
		t.Errorf("OriginURL = %q, want origin's own url", got)
	}
}

// The same fallback rules, across providers: a GitLab remote is a first-class
// candidate, and a GitHub remote plus a GitLab remote is ambiguous — no
// provider is preferred.
func TestOriginURLFallbackAcrossProviders(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name, config, want string
	}{
		{"one-gitlab-remote",
			"[remote \"pushremote\"]\n\turl = deploy@203.0.113.7:team/project.git\n" +
				"[remote \"gitlab\"]\n\turl = git@gitlab.com:example-group/example-project.git\n",
			"git@gitlab.com:example-group/example-project.git"},
		{"one-nested-gitlab-remote",
			"[remote \"lab\"]\n\turl = https://gitlab.com/example-group/sub/example-project.git\n",
			"https://gitlab.com/example-group/sub/example-project.git"},
		{"github-and-gitlab",
			"[remote \"github\"]\n\turl = git@github.com:example-org/example-repo.git\n" +
				"[remote \"gitlab\"]\n\turl = git@gitlab.com:example-org/example-repo.git\n",
			""},
		{"two-different-gitlab",
			"[remote \"a\"]\n\turl = https://gitlab.com/example-group/one\n" +
				"[remote \"b\"]\n\turl = https://gitlab.com/example-group/two\n",
			""},
		// One repository reached two ways is not ambiguous; the smallest
		// remote name's URL is returned, whatever the map order.
		{"same-repo-two-forms",
			"[remote \"zssh\"]\n\turl = git@gitlab.com:example-group/example-project.git\n" +
				"[remote \"ahttps\"]\n\turl = https://gitlab.com/Example-Group/example-project\n",
			"https://gitlab.com/Example-Group/example-project"},
		{"unregistered-host-only",
			"[remote \"bb\"]\n\turl = https://bitbucket.org/example-org/example-repo.git\n",
			""},
		// origin wins even when it is a GitLab remote and another is GitHub.
		{"gitlab-origin-wins",
			"[remote \"github\"]\n\turl = https://github.com/other-org/other-repo\n" +
				"[remote \"origin\"]\n\turl = https://gitlab.com/example-group/example-project\n",
			"https://gitlab.com/example-group/example-project"},
	}
	for _, tc := range cases {
		writeRepo(t, root, tc.name, tc.config)
		for i := 0; i < 20; i++ { // map iteration order must not matter
			if got := OriginURL(filepath.Join(root, tc.name)); got != tc.want {
				t.Errorf("%s: OriginURL = %q, want %q", tc.name, got, tc.want)
				break
			}
		}
	}
}

func TestOrigins(t *testing.T) {
	root := t.TempDir()
	writeRepo(t, root, "gh", "[remote \"origin\"]\n\turl = git@github.com:example-org/example-repo.git\n")
	writeRepo(t, root, "gl", "[remote \"origin\"]\n\turl = https://gitlab.com/example-group/sub/proj.git\n")
	writeRepo(t, root, "gl-fallback", "[remote \"pushremote\"]\n\turl = deploy@203.0.113.7:team/project.git\n[remote \"lab\"]\n\turl = git@gitlab.com:example-group/proj2.git\n")
	writeRepo(t, root, "other", "[remote \"origin\"]\n\turl = https://git.example.test/a/b.git\n")
	got := Origins(root, []string{"gh", "gl", "gl-fallback", "other", "../escape"})
	want := map[string]gitprovider.Ref{
		"gh":          {Provider: "github", Owner: "example-org", Name: "example-repo"},
		"gl":          {Provider: "gitlab", Owner: "example-group/sub", Name: "proj"},
		"gl-fallback": {Provider: "gitlab", Owner: "example-group", Name: "proj2"},
	}
	if len(got) != len(want) {
		t.Fatalf("Origins = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Origins[%q] = %+v, want %+v", k, got[k], v)
		}
	}
	// The legacy wire map keeps GitHub only: an old server would otherwise
	// read a GitLab repository as GitHub.
	legacy := GitHubOnly(got)
	if len(legacy) != 1 || legacy["gh"] != "example-org/example-repo" {
		t.Errorf("GitHubOnly = %v", legacy)
	}
}

func writeRepo(t *testing.T, root, name, config string) {
	t.Helper()
	gd := filepath.Join(root, name, ".git")
	if err := os.MkdirAll(gd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gd, "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRemotes(t *testing.T) {
	root := t.TempDir()
	// Folder name differs from the remote's repository name — the case that
	// makes guessing "<org>/<folder>" wrong.
	writeRepo(t, root, "entertainment", "[remote \"origin\"]\n\turl = git@github.com:example-org/example-repo.git\n")
	writeRepo(t, root, "gitlab-hosted", "[remote \"origin\"]\n\turl = https://gitlab.com/example-org/x.git\n")
	writeRepo(t, root, "no-origin", "[core]\n\tbare = false\n")
	if err := os.MkdirAll(filepath.Join(root, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A linked worktree: .git is a file pointing at <main>/.git/worktrees/wt,
	// whose commondir leads back to the main repo's config.
	wtAdmin := filepath.Join(root, "entertainment", ".git", "worktrees", "wt")
	if err := os.MkdirAll(wtAdmin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtAdmin, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wt", ".git"), []byte("gitdir: "+wtAdmin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Remotes(root, []string{"entertainment", "gitlab-hosted", "no-origin", "not-a-repo", "wt", "missing", "../escape", ".hidden"})
	want := map[string]string{"entertainment": "example-org/example-repo", "wt": "example-org/example-repo"}
	if len(got) != len(want) {
		t.Fatalf("Remotes = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Remotes[%q] = %q, want %q", k, got[k], v)
		}
	}
	if len(Remotes("", []string{"entertainment"})) != 0 {
		t.Error("empty root must yield no remotes")
	}
}

func TestRemotesCapped(t *testing.T) {
	root := t.TempDir()
	names := make([]string, 0, MaxRemotes+5)
	for i := 0; i < MaxRemotes+5; i++ {
		n := "r" + strconv.Itoa(i)
		writeRepo(t, root, n, "[remote \"origin\"]\n\turl = https://github.com/example-org/"+n+"\n")
		names = append(names, n)
	}
	if got := len(Remotes(root, names)); got != MaxRemotes {
		t.Errorf("len(Remotes) = %d, want cap %d", got, MaxRemotes)
	}
}
