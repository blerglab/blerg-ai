// Package gitremote derives a repository's identity on a git hosting service
// (provider + "owner/name") from its local git configuration, without running
// git and without touching the network.
//
// It exists so a daemon can tell the server which hosted repository each
// folder under its repos root actually is: a folder's name need not match its
// remote's repository name, so guessing "<org>/<folder>" is wrong, and a
// cluster session can only clone a repository it can name.
//
// Which hosts are recognised is gitprovider.Default's business — nothing here
// names a service except the legacy GitHub-only helpers. Everything is
// deliberately conservative: a remote that is not plainly a registered
// provider's URL, or whose path fails that provider's naming rules, is
// reported as unknown rather than guessed at.
package gitremote

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

// MaxRemotes caps how many name → repository entries a daemon reports and a
// server accepts per message. Far above any real repos root; it only bounds a
// runaway or hostile directory listing.
const MaxRemotes = 1000

// maxRemoteSections bounds how many distinct [remote "…"] sections a single
// repo's config contributes, so a crafted config can't make the fallback
// scan unbounded.
const maxRemoteSections = 64

// maxConfigBytes bounds how much of a .git/config (or gitdir pointer) is read.
// Real configs are a few hundred bytes to a few KiB.
const maxConfigBytes = 64 << 10

// github is the registry's GitHub provider, for the legacy GitHub-only
// helpers below (and the daemon's legacy repo_remotes report).
func github() gitprovider.Provider {
	p, _ := gitprovider.Default.ProviderForCredentialKind(gitprovider.GitHubID)
	return p
}

// ValidOrgName reports whether s is exactly "org/name" in GitHub's charset,
// and also satisfies the server's repo-name rules (no leading dot in either
// segment, so no "." / ".." and no hidden directories).
func ValidOrgName(s string) bool {
	org, name, ok := strings.Cut(s, "/")
	if !ok || strings.Contains(name, "/") {
		return false
	}
	g := github()
	return g.ValidOwnerName(org) && g.ValidRepoName(name)
}

// ParseGitHub extracts "org/name" from a GitHub remote URL — the GitHub
// provider's ParseRemoteURL, kept for callers that only speak GitHub.
func ParseGitHub(raw string) (string, bool) {
	owner, name, ok := github().ParseRemoteURL(raw)
	if !ok {
		return "", false
	}
	return owner + "/" + name, true
}

// ParseRemote resolves a remote URL against every registered provider.
func ParseRemote(raw string) (gitprovider.Ref, bool) {
	return gitprovider.Default.ParseRemoteURL(raw)
}

// OriginURL returns the url of [remote "origin"] from the git config of the
// repository at repoDir — or, when there is no "origin" remote at all (a repo
// set up with differently-named remotes, e.g. a personal push remote plus a
// "github" remote), the url of the single OTHER remote that parses as a
// registered provider's URL (any provider: GitHub, GitLab, …). Remotes that
// name two DIFFERENT repositories — on one provider or across providers, e.g.
// a GitHub remote and a GitLab remote — are ambiguous (a fork and its
// upstream, a mirror) and left unresolved rather than guessed; no provider is
// preferred over another. Two remotes naming the same repository (https and
// ssh forms of one URL) count as one. It reads the config file directly (no
// git process, so no hooks, includes, or insteadOf rewriting — a rewritten URL
// is simply not recognised, which is the safe outcome). It follows a ".git"
// file's "gitdir:" pointer (worktrees, submodules) and a worktree's
// "commondir". Returns "" when nothing resolves or anything is unreadable.
func OriginURL(repoDir string) string {
	gitDir := filepath.Join(repoDir, ".git")
	fi, err := os.Stat(gitDir)
	if err != nil {
		return ""
	}
	if !fi.IsDir() {
		b, err := readCapped(gitDir)
		if err != nil {
			return ""
		}
		line, _, _ := strings.Cut(string(b), "\n")
		target, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !ok {
			return ""
		}
		target = strings.TrimSpace(target)
		if target == "" {
			return ""
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(repoDir, target)
		}
		gitDir = target
		if cb, err := readCapped(filepath.Join(gitDir, "commondir")); err == nil {
			common := strings.TrimSpace(string(cb))
			if common != "" {
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitDir, common)
				}
				gitDir = common
			}
		}
	}
	b, err := readCapped(filepath.Join(gitDir, "config"))
	if err != nil {
		return ""
	}
	remotes := remoteURLsFromConfig(b)
	if u := remotes["origin"]; u != "" {
		return u
	}
	// No "origin": fall back only when every other remote that is a
	// registered provider's URL names one and the same repository. Anything
	// more is ambiguous — a fork's upstream, a mirror, the same project on
	// two services — so it is left unresolved rather than guessed, same as
	// an unparseable remote. Map order is random, so when one repository is
	// reached through several remotes the URL returned is picked
	// deterministically (the lexically smallest remote name).
	var soleKey, soleURL, soleName string
	for name, u := range remotes {
		ref, ok := ParseRemote(u)
		if !ok {
			continue
		}
		switch {
		case soleKey == "":
			soleKey, soleURL, soleName = ref.Key(), u, name
		case ref.Key() != soleKey:
			return ""
		case name < soleName:
			soleURL, soleName = u, name
		}
	}
	return soleURL
}

func readCapped(p string) ([]byte, error) {
	f, err := os.Open(p) //nolint:gosec // p is <repo>/.git/config under the project dir; read capped by maxConfigBytes
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxConfigBytes))
}

// remoteURLsFromConfig is a minimal git-config reader: it finds the first
// "url" key inside every [remote "<name>"] section and returns name → url.
// Section names are case-insensitive, the subsection (remote name) is
// case-sensitive, keys are case-insensitive. A remote with more than one url
// line (a push mirror set) keeps only the first, which is what git itself
// uses to fetch.
func remoteURLsFromConfig(b []byte) map[string]string {
	out := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(b))
	var current string
	inRemote := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.Index(line, "]")
			if end < 0 {
				inRemote = false
				continue
			}
			hdr := strings.TrimSpace(line[1:end])
			sect, sub, hasSub := strings.Cut(hdr, " ")
			inRemote = hasSub && strings.EqualFold(sect, "remote")
			if inRemote {
				current = strings.Trim(strings.TrimSpace(sub), `"`)
			}
			// A key may follow the header on the same line.
			line = strings.TrimSpace(line[end+1:])
			if line == "" {
				continue
			}
		}
		if !inRemote || current == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "url") {
			continue
		}
		if _, exists := out[current]; exists {
			continue // keep the first url= under this remote, like git does
		}
		if len(out) >= maxRemoteSections {
			continue
		}
		out[current] = unquoteValue(strings.TrimSpace(val))
	}
	return out
}

// unquoteValue strips a trailing comment and surrounding double quotes. It
// does not implement escapes: a value that needs them is not a plain hosted
// repository URL, and every provider's parser rejects what is left.
func unquoteValue(v string) string {
	if strings.HasPrefix(v, `"`) {
		if end := strings.Index(v[1:], `"`); end >= 0 {
			return v[1 : end+1]
		}
		return ""
	}
	if i := strings.IndexAny(v, "#;"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// Origins returns name → provider reference for each of names (directories
// directly under reposRoot) whose origin (see OriginURL) is a registered
// provider's repository. Names that are not plain single path components are
// skipped. At most MaxRemotes entries are returned.
func Origins(reposRoot string, names []string) map[string]gitprovider.Ref {
	out := make(map[string]gitprovider.Ref)
	if reposRoot == "" {
		return out
	}
	for _, n := range names {
		if len(out) >= MaxRemotes {
			break
		}
		if n == "" || n != filepath.Base(n) || strings.HasPrefix(n, ".") {
			continue
		}
		if ref, ok := ParseRemote(OriginURL(filepath.Join(reposRoot, n))); ok {
			out[n] = ref
		}
	}
	return out
}

// GitHubOnly narrows an Origins result to the legacy name → "org/name" map of
// GitHub remotes: the wire shape of the daemon's repo_remotes field, which
// servers that predate providers read as GitHub.
func GitHubOnly(origins map[string]gitprovider.Ref) map[string]string {
	out := make(map[string]string)
	for n, ref := range origins {
		if ref.Provider == gitprovider.GitHubID {
			out[n] = ref.FullName()
		}
	}
	return out
}

// Remotes returns name → org/name for each of names whose origin is a
// recognisable GitHub remote — GitHubOnly(Origins(…)).
func Remotes(reposRoot string, names []string) map[string]string {
	return GitHubOnly(Origins(reposRoot, names))
}
