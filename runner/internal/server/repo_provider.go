package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

// Which callers get the legacy GitHub default for a repo with no provider.
//
// Every start path accepts an optional "provider" (a gitprovider ID). When
// one is given it is honoured strictly on every runtime — the clone URL
// (cluster), the daemon's clone remote (desktop), the token choice — so a
// GitLab "grp/tool" is never cloned from github.com. When none is given the
// repo is read as GitHub, because every caller that can omit it predates
// providers and only ever meant GitHub:
//
//   - POST /api/runner/start and the MCP start_session tool without
//     "provider" (blerg-board cards, agent tokens, scripts written against
//     contract v1, whose repos were always GitHub or the operator's base);
//   - POST /api/sessions without "provider" (older launch sheets, and the
//     current sheet's new-folder entries — its picked repositories and typed
//     owner/name or URL entries always send their provider). Its "clone" and
//     "git_url" (resolveNamedRepo) never get the default: a named clone
//     requires the provider, or takes it from the URL's host;
//   - a cluster resume of a session that recorded no clone URL (rows from
//     before the URL was recorded, all started under the GitHub-only rules);
//   - an older daemon, which ignores SpawnSession.Provider: the server never
//     sends it a non-GitHub provider that would need a clone
//     (DaemonConn.CanCloneFrom).
//
// Any new caller that accepts an owner/name should require a provider
// instead of inheriting this default.

// repoProviderProblem validates an explicit provider for repo. requireFull
// demands owner/name even for GitHub (a clone with nothing else to resolve a
// bare name against). "" = fine.
func repoProviderProblem(provider, repo string, requireFull bool) string {
	if provider == "" {
		return ""
	}
	if _, ok := gitprovider.Default.ProviderForCredentialKind(provider); !ok {
		return "unknown git provider"
	}
	if !strings.Contains(repo, "/") && !requireFull {
		return "" // a folder name: the daemon addresses it by path
	}
	if _, ok := gitprovider.Default.ParseFullName(provider, repo); !ok {
		return "repo must be given as owner/name on " + provider
	}
	return ""
}

// gitURLMatchesProvider reports whether an explicit clone URL is on
// provider's own host. A caller naming both must not have them disagree: the
// provider decides which token goes with the clone.
func gitURLMatchesProvider(gitURL, provider string) bool {
	if gitURL == "" || provider == "" {
		return true
	}
	p, ok := gitprovider.Default.ProviderForCredentialKind(provider)
	return ok && urlHost(gitURL) == p.Host()
}

// resolveNamedRepo normalises the ways POST /api/sessions can name a hosted
// repository to launch on — as opposed to a folder a daemon already has:
//
//   - git_url: a remote URL on a registered provider (https, ssh:// or
//     scp-like; parsed by gitprovider.Default.ParseRemoteURL, never guessed).
//     It fixes both the provider and owner/name; a provider or repo given
//     alongside must agree with it. It implies clone.
//   - clone with repo (owner/name) and provider: both required together —
//     a named clone never inherits the legacy GitHub default.
//
// On success req.Repo is "owner/name", req.Provider is set and req.GitURL is
// consumed. "" = fine (including a request that names neither, which keeps
// today's meaning exactly).
func resolveNamedRepo(req *spawnSessionRequest) string {
	if req.GitURL != "" {
		ref, ok := gitprovider.Default.ParseRemoteURL(req.GitURL)
		if !ok {
			return "git_url must be a repository URL on " + strings.Join(providerHosts(), " or ")
		}
		if req.Provider != "" && req.Provider != ref.Provider {
			return "git_url is not on " + req.Provider + "'s host"
		}
		if req.Repo != "" && !strings.EqualFold(req.Repo, ref.FullName()) {
			return "repo and git_url name different repositories"
		}
		req.Repo, req.Provider, req.Clone, req.GitURL = ref.FullName(), ref.Provider, true, ""
	}
	if !req.Clone {
		return ""
	}
	if req.NewRepo {
		return "new_repo and clone cannot be combined"
	}
	if req.Provider == "" {
		return "clone needs provider together with repo as owner/name"
	}
	if msg := repoProviderProblem(req.Provider, req.Repo, true); msg != "" {
		return msg
	}
	if validateRepoName(req.Repo) != nil {
		// A GitLab subgroup project ("grp/sub/tool"): valid there, but every
		// runner path addresses a repository as at most owner/name.
		return "repositories in nested groups can't be launched yet — use a project directly under its group"
	}
	return ""
}

// providerHosts lists every registered provider's host, for messages.
func providerHosts() []string {
	var out []string
	for _, p := range gitprovider.Default.Providers() {
		out = append(out, p.Host())
	}
	return out
}

// cloneFolderFor picks the folder under daemon d's repos root that a named
// clone of ref uses, from what d last reported. The rule, in order:
//
//  1. a folder named "<name>" or "<owner>-<name>" whose origin d reports as
//     ref itself: that folder, already there (present=true, no clone);
//  2. else the first of "<name>", "<owner>-<name>" that d has no folder of
//     that name at: cloned into;
//  3. else both are taken by something else: problem says so, and nothing
//     is sent.
//
// "<name>" first keeps the folder named like the repository, as every
// daemon clone always was; "<owner>-<name>" is the one fallback, so an
// unrelated folder that happens to share the name is never cloned into or
// over. The daemon re-checks on disk (EnsureClonedTarget) and refuses rather
// than guess if its disk no longer matches this report.
func cloneFolderFor(d *DaemonConn, ref gitprovider.Ref) (folder string, present bool, problem string) {
	candidates := []string{ref.Name, strings.ReplaceAll(ref.Owner, "/", "-") + "-" + ref.Name}
	have := d.CheckedOutRepos()
	for _, c := range candidates {
		if !slices.Contains(have, c) {
			continue
		}
		if o, ok := d.RepoOrigin(c); ok && repoKey(o.Provider, o.FullName) == repoKey(ref.Provider, ref.FullName()) {
			return c, true, ""
		}
	}
	for _, c := range candidates {
		if !slices.Contains(have, c) {
			return c, false, ""
		}
	}
	return "", false, fmt.Sprintf("folders %q and %q on %s already hold other repositories — rename one, or launch on the folder that is %s",
		candidates[0], candidates[1], d.Name, ref.FullName())
}

// namedCloneSpawn prepares a daemon spawn of a named repository (owner/name
// on provider, already validated by resolveNamedRepo): the folder it runs in
// (cloneFolderFor), the repository to clone there, and — only when the clone
// needs it — the caller's own token for it. sandboxed says the session is a
// Local sandbox one. A problem comes with the HTTP status to answer it with.
//
// Who may hold a person's token. On the bare host (This machine) the clone's
// git process runs as the daemon's user, and any other unsandboxed session on
// that machine could read the token from its environment while it runs — so a
// token goes to the host only when the daemon's owner opted in
// (AllowsHostCredentialClone: a daemon nobody else uses). A Local sandbox
// session's clone runs inside the sandbox image instead, with the token kept
// out of every argv and environment (daemon.sandboxGitClone).
//
// When a token is sent at all:
//   - no clone happens (the daemon reports a folder already holding this
//     repository): none is fetched;
//   - the caller holds none for this provider (or core can't say): none, and
//     a public repository still clones;
//   - the provider says, asked with that token, the repository is public:
//     none — a public clone never carries a credential;
//   - it is private: sent for a sandboxed session or an opted-in daemon;
//     otherwise the launch is refused with a reason (422) rather than started
//     to fail;
//   - the provider can't say (API error, or the token can't see it): sent
//     where a token may go, and otherwise left out — the clone then works if
//     the repository is public or the machine's own git access reaches it.
//
// The token is the calling account's personal token for the provider whose
// host the clone URL is on (personalGitKind — the same rule that picks a
// cluster pod's token), never the operator's shared one. It travels in the
// spawn_session message over the daemon's authenticated websocket, and the
// daemon keeps it only for the clone.
func (a *API) namedCloneSpawn(ctx context.Context, d *DaemonConn, provider, repo, accountID string, sandboxed bool) (folder, cloneFrom, token string, status int, problem string) {
	ref, ok := gitprovider.Default.ParseFullName(provider, repo)
	if !ok { // resolveNamedRepo ran first; defence in depth
		return "", "", "", http.StatusUnprocessableEntity, "repo must be given as owner/name on " + provider
	}
	folder, present, problem := cloneFolderFor(d, ref)
	if problem != "" {
		return "", "", "", http.StatusConflict, problem
	}
	if present {
		return folder, ref.FullName(), "", 0, ""
	}
	p, _ := gitprovider.Default.ProviderForCredentialKind(ref.Provider)
	kind := personalGitKind(p.CloneURL(ref.Owner, ref.Name, ""))
	if kind != ref.Provider {
		return folder, ref.FullName(), "", 0, ""
	}
	fetch := a.fetchGitToken
	if fetch == nil {
		fetch = func(ctx context.Context, accountID, kind string) ([]byte, bool, error) {
			// The browser request's own session (carried on ctx), which core
			// checks belongs to accountID.
			return fetchCoreCredential(ctx, nil, a.coreURL, a.coreInternalKey, accountID, kind, coreProofFrom(ctx))
		}
	}
	tok, found, err := fetch(ctx, accountID, kind)
	token = strings.TrimSpace(string(tok))
	if err != nil || !found || token == "" {
		return folder, ref.FullName(), "", 0, ""
	}
	private, known := a.repoPrivate(ctx, p, token, ref)
	mayHoldToken := sandboxed || d.AllowsHostCredentialClone()
	switch {
	case known && !private:
		return folder, ref.FullName(), "", 0, ""
	case known && private && !mayHoldToken:
		return "", "", "", http.StatusUnprocessableEntity, fmt.Sprintf(
			"%s is private, and %s uses your %s token only for a Local sandbox session — on This machine any other session could read it while it clones. Pick Local sandbox, or clone it into %s yourself (or, on a daemon nobody else uses, its owner can set BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE=true)",
			ref.FullName(), d.Name, ref.Provider, d.CurrentReposRoot())
	case !mayHoldToken:
		return folder, ref.FullName(), "", 0, ""
	}
	return folder, ref.FullName(), token, 0, ""
}

// repoPrivate asks the provider, with the caller's own token, whether ref
// needs authentication to clone. known=false when it can't tell.
func (a *API) repoPrivate(ctx context.Context, p gitprovider.Provider, token string, ref gitprovider.Ref) (private, known bool) {
	if a.repoVisibility != nil {
		return a.repoVisibility(ctx, p, token, ref)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	private, err := p.Private(ctx, token, ref.Owner, ref.Name)
	if err != nil {
		// Status only (StatusError never carries a body); never the token.
		log.Printf("named clone: %s visibility of %s unknown: %v", ref.Provider, ref.FullName(), err)
		return false, false
	}
	return private, true
}
