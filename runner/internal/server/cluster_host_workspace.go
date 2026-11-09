package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspacepolicy"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

var oidPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// hostClusterChildWorkspace is a.clusterChildWorkspace's sibling for BLG-501 (design §3): a
// host Main's explicit --runtime cluster child. selectHostClusterChildSelectors
// (workspace_child.go) already canonicalized req.WorkspaceRepos against the parent's own saved
// manifest and required req.Base; this is the network half — resolving each selected member's
// already-known provider repository (protocol.WorkspaceMember.Project, stamped at the host
// workspace's own selection time in workspace_selection.go, never re-derived from a live daemon
// here), checking the required --base is reachable there (design §3.3's accepted-limitation
// check, §9), and handing back read-only-shaped members. It never sets PushAuthorized; a
// credential is minted later, in withClusterWorkspace's own ReadOnlyGitWorker path (§2), never
// here.
func (a *API) hostClusterChildWorkspace(ctx context.Context, parent *db.SessionRow, req runnerStartRequest, childID string, parentManifest protocol.WorkspaceManifest, proof coreProof) (*protocol.WorkspaceManifest, []clusterworkspace.Member, *APIError) {
	fail := func(format string, args ...any) (*protocol.WorkspaceManifest, []clusterworkspace.Member, *APIError) {
		logLaunchFailure("authorization", &jobSubmissionError{reason: "Invalid"})
		return nil, nil, apiErrorf(http.StatusUnprocessableEntity, format, args...)
	}
	jm := a.hub.JobManager()
	if jm == nil {
		return fail("%s", msgClusterNotConfigured)
	}
	if req.WorkspaceLaunch != parentManifest.LaunchGeneration || parentManifest.OwnerID != derefOrEmpty(parent.SpawningAccountID) ||
		len(req.WorkspaceRepos) == 0 || strings.TrimSpace(req.Base) == "" || childID == parent.ID {
		return fail("%s", "parent workspace selection is unavailable, changed, or missing --base")
	}
	selected := make([]protocol.WorkspaceMember, 0, len(req.WorkspaceRepos))
	for _, member := range parentManifest.Members {
		if containsString(req.WorkspaceRepos, member.ID) {
			selected = append(selected, member)
		}
	}
	// Every requested id must have matched a real parent member — never a partial launch.
	if len(selected) != len(req.WorkspaceRepos) {
		return fail("%s", "parent workspace selection is unavailable or changed")
	}

	// Pass 1 (row 2, static, no network): every selected member must already have a
	// resolvable provider remote before any member reaches a network call, and before the
	// manifest below is even built — a member that fails here refuses the whole launch
	// before a single base-branch check runs for any other member, never a partial launch,
	// and never a wasted network call on a request that's refused anyway.
	type resolved struct {
		id, provider, cloneURL, owner, destination, credentialHost, path, sourceIdentity string
	}
	plan := make([]resolved, 0, len(selected))
	for _, m := range selected {
		// Project was stamped at this workspace's own selection time
		// (workspace_selection.go) from the originating desktop daemon's own reported git
		// remote — never re-derived from a live daemon connection here, so this check
		// stands even while the desktop is offline.
		if m.Project == "" || validateRepoName(m.Project) != nil {
			return fail("member %q has no resolvable git remote; select a member that is a hosted repository this account is authorized for", m.ID)
		}
		cloneURL := jm.CloneURLFor(m.Provider, m.Project)
		u, uerr := url.Parse(cloneURL)
		if uerr != nil || u.Host == "" || u.User != nil {
			return fail("member %q has no resolvable git remote", m.ID)
		}
		plan = append(plan, resolved{id: m.ID, provider: m.Provider, cloneURL: cloneURL, owner: gitOwnerFromCloneURL(cloneURL), destination: m.Project, credentialHost: u.Host, path: m.Path, sourceIdentity: m.SourceIdentity})
	}

	// Only a desktop root member changes path; preserve the parent's layout
	// and every non-root path. Validate projection output before indexing it.
	paths, err := projectHostClusterMemberPaths(parentManifest, workspaceselection.MemberPaths)
	if err != nil {
		logClusterRefusal(err)
		return nil, nil, apiErrorCause(422, err, "%s", err.Error())
	}
	clusterPaths := map[string]string{}
	for i, member := range parentManifest.Members {
		clusterPaths[member.ID] = paths[i]
	}
	child := protocol.WorkspaceManifest{Version: protocol.WorkspaceVersion, Kind: "folder", Layout: parentManifest.Layout, OwnerID: parentManifest.OwnerID, Runtime: "cluster", LaunchGeneration: newUUID()}
	for _, member := range selected {
		// A cluster-runtime manifest's Destination is the clone path itself (matching the
		// invariant clusterLaunchMembers already relies on for an ordinary cluster
		// workspace's own members) — never the host folder's internal source identity
		// (SourceIdentity) this field holds for a host-runtime manifest. Every selected
		// member already passed the Project check above, so this is never empty here.
		member.Destination = member.Project
		if member.Path == "." {
			member.Path = clusterPaths[member.ID]
		}
		child.Members = append(child.Members, member)
	}
	if len(child.Members) == 1 {
		child.Kind = "repo"
	}
	raw, merr := json.Marshal(child)
	if merr != nil {
		return fail("%s", "could not build the cluster workspace for this launch")
	}
	sum := sha256.Sum256(raw)
	child.Digest = hex.EncodeToString(sum[:])
	if child.Validate() != nil {
		return fail("%s", "could not build a valid cluster workspace for this launch")
	}

	// The daemon round trip (Main's own local SHA for --base) needs exactly one connection
	// for the whole launch: every selected member lives under the same workspace's desktop.
	// Resolved here, but only checked for nil right before first use below — a member that
	// refuses earlier (credential checks, an absent remote branch) never needs it, and a
	// test that replaces resolveBaseRef never needs a real, DB-registered desktop
	// connection to exercise the rest of this function.
	dc := a.hub.ownedHostDaemon(ctx, parentManifest.OwnerID, parent.DaemonID)

	// Pass 2 (rows 3-5, network): the per-member read-only token, the provider's own
	// credential/branch-exists check, Main's own local commit for --base (design §10: the
	// daemon only ever reads a ref — never an object, never the network), and finally the
	// provider's own compare API to decide whether that exact commit is actually pushed.
	// No git subprocess anywhere in this function; the daemon's own subprocess (resolveBase)
	// never does more than resolve one ref name.
	members := make([]clusterworkspace.Member, 0, len(plan))
	for _, m := range plan {
		token, _, terr := jm.fetchReadOnlyGitToken(ctx, parentManifest.OwnerID, m.provider, m.owner, proof)
		if terr != nil {
			return fail("the read-only token for %q could not be checked right now; retry", m.owner)
		}
		remoteSHA, cred, berr := jm.checkBaseBranchRemote(ctx, m.cloneURL, req.Base, token)
		if berr != nil {
			return fail("the base branch for member %q could not be checked against %s right now; retry", m.id, m.provider)
		}
		switch cred {
		case credentialMissing:
			// Row 4: missing owner token (no credential to even ask the provider with).
			refusal := &clusterAuthorizationRefusal{code: "owner_token_missing"}
			logClusterRefusal(refusal)
			return nil, nil, apiErrorCause(422, refusal, "member %q (owner %q) has no stored read-only token, and its repository could not be read anonymously; add one in Settings → Read-only tokens for workers", m.id, m.owner)
		case credentialRejected:
			// Row 5: a stored token exists but the provider rejected it (expired or
			// otherwise invalid) — never folded into "not pushed".
			refusal := &clusterAuthorizationRefusal{code: "owner_token_rejected"}
			logClusterRefusal(refusal)
			return nil, nil, apiErrorCause(422, refusal, "the read-only token for %q was rejected: check Settings → Read-only tokens for workers", m.owner)
		}
		if remoteSHA == "" {
			// Row 3: the branch has never been pushed to this repository at all.
			refusal := &clusterAuthorizationRefusal{code: "base_branch_not_found"}
			logClusterRefusal(refusal)
			return nil, nil, apiErrorCause(422, refusal, "base branch %q for member %q is not yet pushed to %s; push it first (base_branch_not_found)", req.Base, m.id, m.owner)
		}
		if dc == nil && a.resolveBaseRef == nil {
			return fail("%s", "the desktop running this workspace is disconnected")
		}
		localSHA, rerr := a.resolveBase(ctx, parentManifest.OwnerID, dc, parentManifest.LaunchGeneration, m.path, m.sourceIdentity, req.Base)
		if rerr != nil {
			return fail("could not resolve --base %q locally for member %q on this desktop; retry", req.Base, m.id)
		}
		pushed, ahead, aheadUnknown, cerr := a.checkSHAPushed(ctx, m.provider, m.cloneURL, req.Base, localSHA, token)
		if cerr != nil {
			return fail("could not check whether --base %q is pushed for member %q right now; retry", req.Base, m.id)
		}
		if !pushed {
			// Row 3: Main's local commit for this branch is not (yet) what's pushed —
			// never pushes on Main's behalf.
			if aheadUnknown {
				return fail("base branch not yet pushed: %q for member %q has diverged from origin; push it first", req.Base, m.id)
			}
			return fail("base branch not yet pushed: %q is %d commit(s) ahead of origin for member %q; push it first", req.Base, ahead, m.id)
		}
		members = append(members, clusterworkspace.Member{
			ID: m.id, Provider: m.provider, CloneURL: m.cloneURL, PushURL: m.cloneURL,
			DestinationIdentity: m.destination, CredentialHost: m.credentialHost, PushAuthorized: false,
			// The local branch can lag a pushed remote tip. It only gates unpushed
			// history; the provider advertisement selects this launch's base.
			BaseRef: req.Base, BaseSHA: remoteSHA,
		})
	}
	return &child, members, nil
}

// resolveBase asks dc (Main's own desktop daemon) to resolve branch to its local commit for
// the member identified by sourceIdentity, by reading a ref only (design §10) — never an
// object, never the network, never a configured helper. See the "resolve_base" discovery
// operation (internal/discovery/baseref.go) for what actually runs on the daemon side.
//
// launchGeneration (the parent host workspace's own, design §13) is required: folderPath is
// the manifest's own public, display Path, which workspace_selection.go's MemberPaths
// deliberately collapses to "." for a single-member selection — never a real filesystem path
// relative to the daemon's configured root (which routinely holds several repositories, with
// only one selected). The daemon recovers the real path itself, from its own bound selection
// for launchGeneration, matching sourceIdentity — exactly like every other post-bind daemon
// operation already does (PinBound, bindChild, RestoreBound), never trusting folderPath for
// that.
func (a *API) resolveBase(ctx context.Context, owner string, dc *DaemonConn, launchGeneration, folderPath, sourceIdentity, branch string) (sha string, err error) {
	if a.resolveBaseRef != nil {
		return a.resolveBaseRef(ctx, owner, dc, launchGeneration, folderPath, sourceIdentity, branch)
	}
	res, rerr := a.hub.requestWorkspaceDiscovery(ctx, owner, dc, protocol.DiscoverWorkspace{
		Operation: "resolve_base", ParentLaunchGeneration: launchGeneration,
		Folder: &protocol.SelectionRef{Path: folderPath, SourceIdentity: sourceIdentity},
		Base:   branch,
	})
	// Every exit here answers the launch with the same generic refusal (the caller names
	// the member, never a path or identity) — but silently, this was the one BLG-501 launch
	// step with no log line at all. Named here so a live failure is diagnosable from the
	// server's own log without needing to reproduce it.
	switch {
	case rerr != nil:
		log.Printf("resolve_base: transport error (daemon unreachable or request timed out): %v", rerr)
	case !res.OK:
		log.Printf("resolve_base: daemon refused, reason=%s", res.ErrorCode)
	case !oidPattern.MatchString(res.BaseSHA):
		log.Printf("resolve_base: daemon returned an unusable commit id")
	default:
		return res.BaseSHA, nil
	}
	return "", errDiscoveryUnavailable
}

// The projector's boolean reports a layout fallback, not an error. Names-layout
// projection should never need that fallback. Fail closed on unexpected shape or
// unsafe segments so neither caller can index a missing member or clone at root.
func projectHostClusterMemberPaths(parent protocol.WorkspaceManifest, project func(protocol.DiscoveryResolved, ...workspaceselection.MemberPathOptions) ([]string, bool)) ([]string, error) {
	if parent.Layout == protocol.WorkspaceLayoutStructure {
		return nil, clusterWorkspaceRefused("workspace_layout_invalid")
	}
	sources := make([]protocol.DiscoverySource, len(parent.Members))
	for i, member := range parent.Members {
		sources[i] = protocol.DiscoverySource{ID: member.ID, Name: member.Name}
	}
	paths, fallback := project(protocol.DiscoveryResolved{Layout: protocol.WorkspaceLayoutNames, Members: sources}, workspaceselection.MemberPathOptions{Cluster: true})
	if fallback || len(paths) != len(parent.Members) {
		return nil, clusterWorkspaceRefused("workspace_member_paths_invalid")
	}
	seen := map[string]bool{}
	for i, member := range parent.Members {
		if parent.Layout == "" {
			paths[i] = member.ID
		}
		folded := workspacepolicy.FoldName(paths[i])
		if !workspacepolicy.SafeSegment(paths[i]) || paths[i] == "." || folded == "lost+found" || seen[folded] {
			return nil, clusterWorkspaceRefused("workspace_member_paths_invalid")
		}
		seen[folded] = true
	}
	return paths, nil
}
