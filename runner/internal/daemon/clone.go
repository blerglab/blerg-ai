package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/gitremote"
)

// cloneRemote builds the git remote for a repo on provider (a gitprovider
// ID). Returns "" when there is nothing safe to clone from.
//
// provider "" is the legacy default every caller that predates providers
// gets (board cards, MCP/API starts without a provider, older servers):
// GitHub, exactly as before. An org-qualified repo ("org/name") is used
// verbatim; a bare name is qualified with the daemon's configured org.
//
// Any named provider — "github" included — is honoured strictly: the remote
// is on that provider's own host, the repo must be owner/name valid there,
// and an unknown provider or a bare name yields "" rather than a GitHub
// guess. (A bare name with "github" keeps the org fallback: that is GitHub.)
// A GitLab "grp/tool" therefore never clones github.com/grp/tool.
func cloneRemote(repo, provider, githubOrg string) string {
	if provider == "" || provider == gitprovider.GitHubID {
		if strings.Contains(repo, "/") {
			if provider != "" {
				if _, ok := gitprovider.Default.ParseFullName(provider, repo); !ok {
					return ""
				}
			}
			return fmt.Sprintf("git@github.com:%s.git", repo)
		}
		if githubOrg == "" {
			return ""
		}
		return fmt.Sprintf("git@github.com:%s/%s.git", githubOrg, repo)
	}
	ref, ok := gitprovider.Default.ParseFullName(provider, repo)
	if !ok {
		return ""
	}
	p, _ := gitprovider.Default.ProviderForCredentialKind(ref.Provider)
	return fmt.Sprintf("git@%s:%s.git", p.Host(), ref.FullName())
}

// EnsureCloned checks whether reposRoot/repo exists and, if not, clones it
// from the remote cloneRemote derives into reposRoot/repo. ctx cancels the
// clone (a kill of the spawn waiting on it).
//
// When a provider is named and the folder already exists, its origin must
// not contradict it: an existing checkout whose remote resolves to another
// provider (or, for owner/name, another repository) is refused rather than
// used — the same path can be a GitHub and a GitLab repository. A folder
// whose origin can't be resolved is used as before.
func EnsureCloned(ctx context.Context, reposRoot, repo, provider, githubOrg string) error {
	dest := filepath.Join(reposRoot, repo)

	if _, err := os.Stat(dest); err == nil {
		if provider != "" {
			if ref, ok := gitremote.ParseRemote(gitremote.OriginURL(dest)); ok {
				mismatch := ref.Provider != provider
				if !mismatch && strings.Contains(repo, "/") {
					mismatch = !strings.EqualFold(ref.FullName(), repo)
				}
				if mismatch {
					return fmt.Errorf("folder %q is %s repository %s, not the requested %s repository", repo, ref.Provider, ref.FullName(), provider)
				}
			}
		}
		return nil
	}

	remote := cloneRemote(repo, provider, githubOrg)
	if remote == "" {
		if provider != "" && provider != gitprovider.GitHubID {
			return fmt.Errorf("cannot clone %q: give it as owner/name on %s", repo, provider)
		}
		return fmt.Errorf("cannot clone %q: no GitHub org configured on this daemon and the repo is not org-qualified", repo)
	}

	return gitClone(ctx, 5*time.Minute, remote, dest, nil)
}

// Clone attempts fail, rather than wait, on anything that would otherwise
// hang a spawn (and every spawn queued behind it — the daemon starts them one
// at a time):
//
//   - no prompt, ever: GIT_TERMINAL_PROMPT=0 (and Git Credential Manager's
//     own), and ssh in BatchMode with a connect timeout, so an unknown host
//     key, a passphrase or a password question is an error, not a wait on a
//     terminal nobody watches;
//   - a transfer that stalls while its connection stays open aborts after
//     lowSpeedTime seconds below lowSpeedLimit bytes/s (git's
//     GIT_HTTP_LOW_SPEED_*), long before the attempt's own timeout.
var (
	lowSpeedLimit     = "1000" // bytes/s
	lowSpeedTime      = "60"   // seconds
	sshConnectTimeout = "20"   // seconds
)

func gitSafetyEnv() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=" + sshConnectTimeout,
		"GIT_HTTP_LOW_SPEED_LIMIT=" + lowSpeedLimit,
		"GIT_HTTP_LOW_SPEED_TIME=" + lowSpeedTime,
	}
}

// errCloneTimedOut marks an attempt that ran out its own timeout, and
// errCloneCancelled one whose spawn was killed. Neither is worth retrying
// over another transport.
var (
	errCloneTimedOut  = errors.New("clone timed out")
	errCloneCancelled = errors.New("clone cancelled: the session was stopped")
)

// runClone runs cmd — a clone — under ctx and timeout. Cancellation (a kill,
// or the timeout) kills cmd's whole process group — git forks
// git-remote-https / ssh helpers that a kill of git alone would leave
// running — plus whatever onCancel stops (a clone container), and waits at
// most a few seconds more for them to let go of cmd's output.
func runClone(ctx context.Context, timeout time.Duration, cmd *exec.Cmd, what string, onCancel func()) error {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", what, err, strings.TrimSpace(stderr.String()))
		}
		return nil
	case <-actx.Done():
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if onCancel != nil {
		onCancel()
	}
	<-done
	if ctx.Err() != nil {
		return errCloneCancelled
	}
	return fmt.Errorf("%s: %w after %s", what, errCloneTimedOut, timeout)
}

// gitClone runs `git clone remote dest` on the host with the daemon's
// sanitized environment, gitSafetyEnv and extraEnv. The remote never carries
// a token (a token rides in extraEnv, see tokenHeaderEnv), and RedactURL
// keeps it that way in the error should a token-bearing URL ever be passed
// here.
func gitClone(ctx context.Context, timeout time.Duration, remote, dest string, extraEnv []string) error {
	cmd := exec.Command("git", "clone", "--", remote, dest) //nolint:gosec,noctx // literal git binary; remote is a clone URL placed after -- and never carries a token; noctx: runClone owns the context and timeout and kills the process group itself
	// git hooks/helpers must not see the master token.
	cmd.Env = sanitizedEnviron(append(gitSafetyEnv(), extraEnv...)...)
	return runClone(ctx, timeout, cmd, "git clone "+gitprovider.RedactURL(remote), nil)
}

// cloneTargetTimeout bounds one attempt of a named-repository clone. Larger
// than EnsureCloned's: a repository someone names by hand can be a big one
// (a kernel tree). A stalled transfer does not get to use it (lowSpeedTime),
// and a kill ends it at once.
var cloneTargetTimeout = 30 * time.Minute

// CloneTarget is a hosted repository to clone into a named folder: what a
// spawn's CloneFrom asks for.
type CloneTarget struct {
	Provider string // gitprovider ID; required
	FullName string // "owner/name" on Provider
	Folder   string // one folder name under the repos root
	// Token is the launching account's personal token for Provider, or ""
	// (a public repository, or no token). Used for this clone only.
	Token string
	// Sandbox runs a token clone inside the sandbox image rather than on the
	// host (the session is a Local sandbox one). HostTokenAllowed lets a
	// token clone run on the host (the daemon's owner opted in: nobody else
	// uses this machine). A token clone with neither is refused: on the host,
	// any other unsandboxed session could read the token out of git's
	// process environment while it runs.
	Sandbox          bool
	HostTokenAllowed bool
}

// cloneTargetGit and cloneInSandbox are the two ways EnsureClonedTarget runs
// one attempt — seams for tests; production is gitClone / sandboxGitClone.
var (
	cloneTargetGit = gitClone
	cloneInSandbox = sandboxGitClone
)

// EnsureClonedTarget makes reposRoot/t.Folder a checkout of t.FullName on
// t.Provider:
//
//   - the folder exists and its origin is that repository: used as it is;
//   - the folder exists and is anything else (another repository, another
//     provider's same-named one, no resolvable origin, not a directory):
//     refused — a named clone never lands in or over an unrelated folder;
//   - the folder is absent: cloned, into a hidden sibling first and renamed
//     into place only once complete, so a clone that fails or is cut off
//     never leaves a half-cloned folder a later spawn would take for the
//     real thing (and sweepCloneLeftovers removes a sibling a killed daemon
//     left behind).
//
// The clone goes over HTTPS to the provider's own host. Without a token it
// runs on the host, which is all a public repository needs. With one it runs
// inside the sandbox image when t.Sandbox (sandboxGitClone), on the host only
// when t.HostTokenAllowed (tokenHeaderEnv), and is refused otherwise; either
// way the token is scoped to that host and never in argv, the URL, the
// clone's .git/config or a log. If HTTPS fails for any reason but its own
// timeout or a kill, the provider's ssh remote is tried on the host with the
// machine's own keys, as every daemon clone before this did (never with the
// token). The folder's origin is the token-free HTTPS URL.
func EnsureClonedTarget(ctx context.Context, reposRoot string, t CloneTarget) error {
	if t.Provider == "" {
		return errors.New("a named clone needs its git provider")
	}
	ref, ok := gitprovider.Default.ParseFullName(t.Provider, t.FullName)
	if !ok {
		return fmt.Errorf("cannot clone %q: give it as owner/name on %s", t.FullName, t.Provider)
	}
	p, _ := gitprovider.Default.ProviderForCredentialKind(ref.Provider)
	if t.Folder == "" || strings.ContainsAny(t.Folder, "/\\") || strings.HasPrefix(t.Folder, ".") {
		return fmt.Errorf("invalid folder name %q", t.Folder)
	}
	dest, err := resolveProjectPath(reposRoot, t.Folder)
	if err != nil {
		return err
	}

	if _, err := os.Lstat(dest); err == nil {
		got, ok := gitremote.ParseRemote(gitremote.OriginURL(dest))
		if ok && got.Key() == ref.Key() {
			return nil
		}
		what := "a folder with no recognisable origin"
		if ok {
			what = fmt.Sprintf("%s repository %s", got.Provider, got.FullName())
		}
		return fmt.Errorf("folder %q under the repos root already holds %s, not %s repository %s — rename or remove it, or launch on that folder instead",
			t.Folder, what, ref.Provider, ref.FullName())
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("folder %q: %w", t.Folder, err)
	}

	if t.Token != "" && !t.Sandbox && !t.HostTokenAllowed {
		return fmt.Errorf("%s is private and this daemon does not use personal tokens outside the sandbox — launch it in Local sandbox, or clone it into the repos folder yourself", ref.FullName())
	}
	if t.Token != "" && !t.Sandbox {
		if err := hostGitSupportsTokenClone(ctx); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("prepare clone of %s: %w", ref.FullName(), err)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), cloneTmpPrefix)
	if err != nil {
		return fmt.Errorf("prepare clone of %s: %w", ref.FullName(), err)
	}
	defer func() { _ = os.RemoveAll(tmp) }() // empty after the rename; a failed clone's leftovers otherwise
	into := filepath.Join(tmp, "repo")

	httpsURL := p.CloneURL(ref.Owner, ref.Name, "") // never carries the token
	var httpsErr error
	switch {
	case t.Token != "" && t.Sandbox:
		httpsErr = cloneInSandbox(ctx, cloneTargetTimeout, httpsURL, tmp, p, t.Token)
	case t.Token != "":
		httpsErr = cloneTargetGit(ctx, cloneTargetTimeout, httpsURL, into, tokenHeaderEnv(p, t.Token))
	default:
		httpsErr = cloneTargetGit(ctx, cloneTargetTimeout, httpsURL, into, nil)
	}
	if httpsErr != nil {
		if errors.Is(httpsErr, errCloneCancelled) {
			return errCloneCancelled
		}
		msg := scrubSecret(httpsErr.Error(), t.Token)
		if errors.Is(httpsErr, errCloneTimedOut) {
			// Its own timeout: the repository is reachable but too slow or
			// too big for the time allowed — ssh would only spend as long
			// again, holding up every spawn behind it.
			return fmt.Errorf("could not clone %s repository %s in time:\n%s", ref.Provider, ref.FullName(), msg)
		}
		_ = os.RemoveAll(into)
		sshURL := fmt.Sprintf("git@%s:%s.git", p.Host(), ref.FullName())
		if sshErr := cloneTargetGit(ctx, cloneTargetTimeout, sshURL, into, nil); sshErr != nil {
			if errors.Is(sshErr, errCloneCancelled) {
				return errCloneCancelled
			}
			return fmt.Errorf("could not clone %s repository %s — a private repository needs your %s token in Settings, or this machine's own access to it:\n%s\n%s",
				ref.Provider, ref.FullName(), ref.Provider, msg, scrubSecret(sshErr.Error(), t.Token))
		}
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("folder %q appeared under the repos root while %s was cloning — not replacing it", t.Folder, ref.FullName())
	}
	if err := os.Rename(into, dest); err != nil {
		return fmt.Errorf("move clone of %s into place: %w", ref.FullName(), err)
	}
	return nil
}

// sandboxCloneScript runs inside the sandbox image. The Basic credential
// arrives on stdin and is written, by the shell's builtin printf, into a
// .gitconfig in a fresh HOME private to this container — so it is never in
// any process's argv or environment, on the host or in the container, and
// needs no particular git version. $1 is the host the header is scoped to,
// $2 the clone URL; the clone lands in /clone/repo (the host's hidden
// sibling folder).
const sandboxCloneScript = `set -eu
IFS= read -r cred
home=$(mktemp -d)
printf '[http "https://%s/"]\n\textraHeader = Authorization: Basic %s\n' "$1" "$cred" > "$home/.gitconfig"
cred=
export HOME="$home"
exec git clone -- "$2" /clone/repo`

// sandboxGitClone clones remote into tmp/repo inside a throwaway sandbox
// container (the same image and hardening a Local sandbox session gets),
// with p's token as an Authorization header for p's host only. The token
// travels over the docker CLI's stdin — not in its argv or environment.
func sandboxGitClone(ctx context.Context, timeout time.Duration, remote, tmp string, p gitprovider.Provider, token string) error {
	var rb [6]byte
	_, _ = rand.Read(rb[:])
	name := "blerg-clone-" + hex.EncodeToString(rb[:])
	args := []string{
		"run", "--rm", "-i", "--name", name,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", "512", "--memory", "4g",
		"--mount", bindMountArg(tmp, "/clone"),
	}
	for _, e := range gitSafetyEnv() {
		args = append(args, "-e", e)
	}
	args = append(args, sandboxImage(), "sh", "-c", sandboxCloneScript, "clone", p.Host(), remote)
	cmd := exec.Command("docker", args...) //nolint:gosec,noctx // literal docker binary, argv assembled by the daemon (no shell); container name is generated, remote and image come from validated config; noctx: runClone owns the context and timeout, and its cleanup callback removes the container
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString([]byte(p.TokenUser()+":"+token)) + "\n")
	return runClone(ctx, timeout, cmd, "git clone "+gitprovider.RedactURL(remote)+" (in the sandbox)", func() {
		_ = exec.Command("docker", "rm", "-f", name).Run() //nolint:gosec,noctx // literal docker binary; name is 'blerg-clone-' plus random hex generated above; noctx: cleanup after cancellation must still run, so it is not bound to the cancelled context
	})
}

// bindMountArg is a read-write --mount value for hostPath at dst, CSV-quoted
// the way docker's parser reads it when the path holds a comma or quote (the
// same rule as cliMountArg).
func bindMountArg(hostPath, dst string) string {
	src := "src=" + hostPath
	if strings.ContainsAny(hostPath, `,"`) {
		src = `"` + strings.ReplaceAll(src, `"`, `""`) + `"`
	}
	return "type=bind," + src + ",dst=" + dst
}

// tokenHeaderEnv is the environment that makes one git command on the host
// authenticate to p's host with token: an http.<url>.extraHeader carrying
// Basic auth (p's token username : token), passed as GIT_CONFIG_COUNT/KEY/
// VALUE so it lives only in that process's environment — not in its argv
// (readable by every user through ps), not in the clone's .git/config, not in
// a URL git could echo. The key is scoped to https://<host>/, so git sends
// the header to that host only, never to a redirect elsewhere. Needs git
// 2.31 or later (hostGitSupportsTokenClone). The result is a secret: never
// log it.
func tokenHeaderEnv(p gitprovider.Provider, token string) []string {
	basic := base64.StdEncoding.EncodeToString([]byte(p.TokenUser() + ":" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://" + p.Host() + "/.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + basic,
	}
}

// hostGitSupportsTokenClone reports whether the host's git reads
// GIT_CONFIG_COUNT (2.31+). An older git ignores it silently and the clone
// then fails as if the token were wrong; this says what is actually missing
// instead. Asked on each host token clone (git can be upgraded under a
// running daemon; `git --version` costs milliseconds against a clone).
func hostGitSupportsTokenClone(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "--version").Output()
	if err != nil {
		return fmt.Errorf("git is not available on this daemon: %w", err)
	}
	if major, minor, ok := parseGitVersion(string(out)); ok && (major < 2 || (major == 2 && minor < 31)) {
		return fmt.Errorf("git 2.31 or newer is needed to clone a private repository with your token (this daemon has %s) — update git, or launch in Local sandbox",
			strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "git version")))
	}
	return nil
}

var gitVersionRe = regexp.MustCompile(`git version (\d+)\.(\d+)`)

// parseGitVersion reads `git --version` output ("git version 2.34.1", also
// vendor suffixes like "(Apple Git-146)"). ok=false when it can't tell.
func parseGitVersion(s string) (major, minor int, ok bool) {
	m := gitVersionRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, true
}

// cloneTmpPrefix names the hidden sibling a named clone is made in.
const cloneTmpPrefix = ".blerg-clone-"

// cloneLeftoverAge is how old a clone's hidden sibling must be before a
// daemon start removes it: well past any attempt this daemon could still be
// running (two of cloneTargetTimeout).
const cloneLeftoverAge = 2 * time.Hour

// sweepCloneLeftovers removes the hidden clone siblings a daemon killed
// mid-clone left under root (its in-process cleanup never ran). Only
// directories named like one and older than cloneLeftoverAge, so a clone in
// progress is never touched.
func sweepCloneLeftovers(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), cloneTmpPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < cloneLeftoverAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			log.Printf("daemon: removing clone leftover %s: %v", e.Name(), err)
		}
	}
}

// scrubSecret removes secret, and every provider's Basic-auth encoding of it,
// from s. git does not echo the header; this is the belt to those braces for
// text that becomes the session's error reason in a browser.
func scrubSecret(s, secret string) string {
	if strings.TrimSpace(secret) == "" {
		return s
	}
	s = strings.ReplaceAll(s, secret, "REDACTED")
	for _, p := range gitprovider.Default.Providers() {
		s = strings.ReplaceAll(s, base64.StdEncoding.EncodeToString([]byte(p.TokenUser()+":"+secret)), "REDACTED")
	}
	return s
}
