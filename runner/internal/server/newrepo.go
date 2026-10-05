package server

// "New repository" on the cluster: the launch sheet names an owner/name that
// does not exist yet. The server creates it on the person's git provider with
// their own stored token, the pod clones it, and the session proceeds like
// any other. When it cannot be created — no token, a token without the
// permission, a provider error — the session still starts: the pod initialises
// an empty repository whose origin is where the repository would be, the
// start panel says why, and the agent is told (runner.PrepareWorkspace).
//
// What never happens: an existing repository is never written into. A
// visibility check that can see the repository answers 409; the create call's
// own "already exists" (gitprovider.ErrExists) is a 409 too, never a fallback
// — that second check is the one that matters, because a 404 (GitHub) or an
// unparsable answer (GitLab) from the first means only "cannot tell"; and the
// pod of a new-repository session never gets the operator's shared git token
// (SessionJobSpec.NoOperatorGitToken), so it can reach only what the person's
// own token can.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// newRepoHostProblem is non-empty when cloneURL points somewhere other than the host the repository would be
// created on, which happens when the operator sets BLERG_RUNNER_AGENT_GIT_BASE for GitHub names.
func newRepoHostProblem(cloneURL, providerID string) string {
	if providerID == "" {
		providerID = gitprovider.GitHubID
	}
	p, ok := gitprovider.Default.ProviderForCredentialKind(providerID)
	if !ok {
		return ""
	}
	if urlHost(cloneURL) != strings.ToLower(p.Host()) {
		return "this install clones from a different git host than " + p.Host() + ", so a new repository cannot be created there; create it first and launch it as an existing repository"
	}
	return ""
}

// createClusterRepo creates req.Repo on req.Provider for accountID and returns
// the start-plan stage describing the outcome: done when created, warning
// (with the reason and what to do) when the session will start without it. A
// repository that already exists is a 409 and the session does not start.
func (a *API) createClusterRepo(ctx context.Context, req spawnSessionRequest, accountID string) (protocol.StartStage, *APIError) {
	provider := req.Provider
	if provider == "" {
		provider = gitprovider.GitHubID
	}
	ref, ok := gitprovider.Default.ParseFullName(provider, req.Repo)
	if !ok {
		return protocol.StartStage{}, apiErrorf(http.StatusUnprocessableEntity, "repo must be given as owner/name on %s", provider)
	}
	p, _ := gitprovider.Default.ProviderForCredentialKind(ref.Provider)
	private := req.Visibility != "public"
	where := p.Host() + "/" + ref.FullName()
	visibility := "private"
	if !private {
		visibility = "public"
	}
	hint := fmt.Sprintf("The repository exists only in this pod until it is pushed. Create %s on %s and ask the agent to push, "+
		"or add a %s token with permission to create repositories in Settings and launch again.", ref.FullName(), p.Host(), ref.Provider)
	warn := func(reason string) protocol.StartStage {
		return protocol.StartStage{ID: protocol.StageRepo, Label: "Creating repository", State: protocol.StageStateWarning,
			Detail: "not created: " + reason, Hint: hint}
	}

	token := a.gitTokenFor(ctx, accountID, ref.Provider)
	if token == "" {
		return warn("no " + ref.Provider + " token in Settings"), nil
	}
	// Exists already? Then it is not new: never create over it, never fall
	// back into it. (An unknown answer proceeds: the create call catches the
	// case where the token cannot see an existing repository.)
	if _, known := a.repoPrivate(ctx, p, token, ref); known {
		return protocol.StartStage{}, apiErrorf(http.StatusConflict, "%s already exists on %s — launch it as an existing repository", ref.FullName(), p.Host())
	}
	create := a.createRepo
	if create == nil {
		create = func(ctx context.Context, p gitprovider.Provider, token string, ref gitprovider.Ref, private bool) error {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			return p.CreateRepo(cctx, token, ref.Owner, ref.Name, private)
		}
	}
	err := create(ctx, p, token, ref, private)
	switch {
	case err == nil:
		log.Printf("new repository %s created on %s (%s)", ref.FullName(), p.Host(), visibility)
		return protocol.StartStage{ID: protocol.StageRepo, Label: "Creating repository", State: protocol.StageStateDone,
			Detail: where + " · " + visibility}, nil
	case errors.Is(err, gitprovider.ErrExists):
		return protocol.StartStage{}, apiErrorf(http.StatusConflict, "%s already exists on %s — launch it as an existing repository", ref.FullName(), p.Host())
	}
	var se *gitprovider.StatusError
	reason := ref.Provider + " could not be reached"
	if errors.As(err, &se) {
		switch se.Status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			reason = fmt.Sprintf("your %s token cannot create repositories under %s", ref.Provider, ref.Owner)
		default:
			reason = fmt.Sprintf("%s returned %d", ref.Provider, se.Status)
		}
	}
	// Status only, never the body, never the token.
	log.Printf("new repository %s on %s: not created: %s (%v)", ref.FullName(), p.Host(), reason, err)
	return warn(reason), nil
}

// gitTokenFor is the account's own token for provider kind ("" when it has
// none or core is not wired), through the same seam namedCloneSpawn uses.
func (a *API) gitTokenFor(ctx context.Context, accountID, kind string) string {
	fetch := a.fetchGitToken
	if fetch == nil {
		fetch = func(ctx context.Context, accountID, kind string) ([]byte, bool, error) {
			return fetchCoreCredential(ctx, nil, a.coreURL, a.coreInternalKey, accountID, kind, coreProofFrom(ctx))
		}
	}
	tok, found, err := fetch(ctx, accountID, kind)
	if err != nil || !found {
		return ""
	}
	return strings.TrimSpace(string(tok))
}
