package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/gitremote"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var membershipClones = struct {
	sync.Mutex
	active map[string]bool
}{active: make(map[string]bool)}
var membershipCloneSlots = make(chan struct{}, 2)

func membershipCloneFailure(stage string, err error) {
	failurelog.Default.Event("membership_clone:"+stage, slog.LevelWarn, "component", "runner", "operation", "membership_clone", "stage", stage, "cause", failurelog.Cause(err))
}

func membershipCloneRefusal(code string) string {
	switch code {
	case "clone_failed":
		return "Git could not complete the clone. Check your Git access, network connection and available disk space."
	case "clone_environment":
		return "The desktop could not prepare its certificate configuration for the sandbox clone. Check its CA settings, then try again."
	case "destination_unavailable":
		return "The workspace folder changed or is no longer available. No repository was cloned."
	case "destination_exists":
		return "The clone destination already contains a different repository or folder. Move it before trying again."
	case "local_storage":
		return "The desktop could not prepare or save the clone destination. Check folder permissions and available disk space."
	case "stale":
		return "The workspace changed before the cloned repository could be added."
	case "outside_folder":
		return msgMembershipOutside
	case "overlapping_repositories", "submodule":
		return msgMembershipOverlap
	case "too_many_members":
		return msgMembershipLimit
	case "invalid_selection":
		return "The desktop rejected the repository request as invalid."
	default:
		return "The desktop could not resolve the cloned repository for this workspace."
	}
}

func membershipCloneChannelRefusal(err error) string {
	if errors.Is(err, errMembershipCloneTimeout) {
		return "The desktop did not confirm clone completion within the time limit. Try again."
	}
	return "" // Disconnects and capacity contention are retried by the sweep.
}

// Keep the existing personal-token boundary: public clones carry no token,
// and a host clone uses one only on an opted-in desktop.
func (a *API) membershipCloneToken(ctx context.Context, dc *DaemonConn, runtime string, ref gitprovider.Ref, token []byte) (string, string) {
	p, _ := gitprovider.Default.ProviderForCredentialKind(ref.Provider)
	value := strings.TrimSpace(string(token))
	private, known := false, false
	if a.repoVisibility != nil {
		private, known = a.repoVisibility(ctx, p, value, ref)
	} else {
		lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var err error
		private, err = p.Private(lookup, value, ref.Owner, ref.Name)
		known = err == nil
		if err != nil {
			membershipCloneFailure("visibility", err)
		}
	}
	mayHold := runtime == "sandbox" || dc.AllowsHostCredentialClone()
	if known && private && !mayHold {
		return "", "This repository needs your Git token. This desktop can use personal tokens only in a Local sandbox workspace."
	}
	if known && !private || !mayHold {
		return "", ""
	}
	return value, ""
}

// Bind the resolved local source to the clone the owner approved, including
// the descriptor identities. A replacement between discovery calls is refused.
func membershipCloneMatches(ref gitprovider.Ref, source protocol.DiscoverySource, resolved *db.MembershipOperation) bool {
	if source.Origin == nil || source.Origin.Provider != ref.Provider || !strings.EqualFold(source.Origin.FullName, ref.FullName()) || resolved.MemberID != source.ID || resolved.Source.SourceIdentity != source.SourceIdentity {
		return false
	}
	for _, member := range resolved.Proposed.Members {
		if member.ID == resolved.MemberID {
			return member.CommonDirectoryIdentity == source.CommonDirectoryIdentity && (member.Provider == "" || member.Provider == ref.Provider && strings.EqualFold(member.Project, ref.FullName()))
		}
	}
	return false
}

// No URL proposal is offered to the runtime before its approved clone has been
// resolved into descriptor-pinned local membership input.
func (a *API) resolveMembershipClone(ctx context.Context, id string) {
	membershipClones.Lock()
	if membershipClones.active[id] {
		membershipClones.Unlock()
		return
	}
	membershipClones.active[id] = true
	membershipClones.Unlock()
	defer func() {
		membershipClones.Lock()
		delete(membershipClones.active, id)
		membershipClones.Unlock()
	}()
	op, err := db.OpenMembershipOperation(ctx, a.dbPool, id)
	if err != nil {
		membershipCloneFailure("read_operation", err)
		return
	}
	if op == nil || op.Phase != db.MembershipAuthorized || op.Source.GitURL == "" {
		return
	}
	ref, ok := gitremote.ParseAddition(op.Source.GitURL)
	row, err := db.GetSession(ctx, a.dbPool, id)
	if !ok {
		membershipCloneFailure("invalid_url", nil)
		return
	}
	if err != nil {
		membershipCloneFailure("read_session", err)
		return
	}
	if row == nil {
		return
	}
	w, err := db.GetSessionWorkspace(ctx, a.dbPool, id)
	if err != nil {
		membershipCloneFailure("read_workspace", err)
		return
	}
	if w == nil || w.Manifest == nil {
		return
	}
	m := *w.Manifest
	refuse := func(reason, cause string) {
		membershipCloneFailure(cause, nil)
		if err := db.RefuseMembershipOperation(ctx, a.dbPool, op.ID, reason); err != nil {
			membershipCloneFailure("save_refusal", err)
		}
		a.hub.membershipChanged(ctx, a.dbPool, id)
	}
	now := time.Now()
	if a.membershipCloneNow != nil {
		now = a.membershipCloneNow()
	}
	retry := func(reason, stage string, cause error) {
		membershipCloneFailure(stage, cause)
		if err := db.SetMembershipCloneWaiting(ctx, a.dbPool, op.ID, now, reason); err != nil {
			membershipCloneFailure("save_retry", err)
			return
		}
		a.hub.broadcastMembership(ctx, a.dbPool, id)
	}
	if row.EndedAt != nil || row.Status == "stopped" || row.Status == "ended" {
		refuse("This workspace has ended. No repository was cloned.", "ended_workspace")
		return
	}
	if m.Runtime == "cluster" {
		refuse(msgMembershipCluster, "cluster_workspace")
		return
	}
	if m.Revision != op.ExpectedRevision || m.Digest != op.OldDigest {
		refuse(msgMembershipStale, "stale_workspace")
		return
	}
	if op.CloneRetryStartedAt != nil && !now.Before(op.CloneRetryStartedAt.Add(15*time.Minute)) {
		refuse("The repository request stopped after 15 minutes. Last status: "+op.Waiting+" Resolve that issue and try again.", "recovery_deadline")
		return
	}
	dc := a.hub.FindDaemonForSession(id)
	if dc != nil && dc.RepositoryCloneVersion != protocol.RepositoryCloneVersion {
		refuse("Update Blerg on this desktop to add repositories by Git URL (reinstall its daemon).", "clone_update_required")
		return
	}
	select {
	case membershipCloneSlots <- struct{}{}:
		defer func() { <-membershipCloneSlots }()
	default:
		retry("Waiting for another repository clone to finish.", "clone_capacity", nil)
		return
	}
	proof := a.sessionLaunchProof(ctx, id)
	lookup := withCoreProof(ctx, proof)
	kinds, lookupErr := listCorePersonalKinds(lookup, nil, a.coreURL, a.coreInternalKey, m.OwnerID, proof)
	if errors.Is(lookupErr, errCorePrincipalRefused) {
		refuse("The sign-in that authorized this workspace is no longer valid. Sign in and resume the workspace, then request the repository again.", "authorization_refused")
		return
	}
	if lookupErr != nil {
		retry("Waiting for your Git authorization to be verified.", "connection_lookup_unavailable", lookupErr)
		return
	}
	connected := false
	for _, kind := range kinds {
		connected = connected || kind == ref.Provider
	}
	if !connected {
		refuse("Connect this Git host in Settings before adding its repositories.", "host_not_connected")
		return
	}
	if dc == nil {
		retry("Waiting for this desktop to reconnect.", "desktop_unavailable", nil)
		return
	}
	token, found, err := fetchCoreCredential(ctx, nil, a.coreURL, a.coreInternalKey, m.OwnerID, ref.Provider, proof)
	if err != nil {
		if errors.Is(err, errNoLivenessProof) || errors.Is(err, errCoreManagedCredential) || errors.Is(verifyCorePrincipal(lookup, nil, a.coreURL, a.coreInternalKey, m.OwnerID, proof), errCorePrincipalRefused) {
			refuse("Your Git connection is no longer authorized for this workspace. Sign in and resume it, then try again.", "credential_refused")
			return
		}
		retry("Waiting for your Git connection to become available.", "credential_lookup_unavailable", err)
		return
	}
	if !found || len(token) == 0 {
		refuse("Could not read your Git connection. Reconnect it in Settings and try again.", "credential_unavailable")
		return
	}
	cloneToken, problem := a.membershipCloneToken(lookup, dc, m.Runtime, ref, token)
	if problem != "" {
		refuse(problem, "host_token_policy")
		return
	}
	folder := gitremote.AdditionFolder(ref)
	res, err := a.hub.requestWorkspaceDiscovery(ctx, m.OwnerID, dc, protocol.DiscoverWorkspace{Operation: "clone_addition", GitURL: op.Source.GitURL, GitToken: cloneToken, Path: folder, ParentLaunchGeneration: m.LaunchGeneration, Runtime: m.Runtime})
	if err != nil || !res.OK || res.Addition == nil {
		if err != nil {
			if reason := membershipCloneChannelRefusal(err); reason != "" {
				refuse(reason, "clone_reply_timeout")
				return
			}
			retry("Waiting for the desktop repository clone service.", "clone_channel_unavailable", err)
			return
		}
		if res.ErrorCode == "binding_pending" {
			retry("Waiting for this workspace to resume on the desktop.", "binding_pending", nil)
			return
		}
		refuse(membershipCloneRefusal(res.ErrorCode), "clone_unavailable")
		return
	}
	c := &membershipContext{caller: delegationCaller{SessionID: id}, row: row, m: m, cloneAddition: res.Addition}
	resolved, issue := a.buildMembership(lookup, c, membershipChangeBody{OperationID: op.ID, Kind: "add", ExpectedRevision: m.Revision, Repository: res.Addition.Source.Path})
	if issue != nil {
		refuse(issue.Message, "addition_unavailable")
		return
	}
	if !membershipCloneMatches(ref, res.Addition.Source, resolved) {
		refuse("The cloned repository changed before it could be added. Try again.", "source_changed")
		return
	}
	resolved.MemberName = gitremote.AdditionName(ref)
	if err := db.ResolveMembershipClone(ctx, a.dbPool, op.ID, op.Proposed.Digest, *resolved); err != nil {
		retry("Waiting to save the completed repository addition.", "save_source", err)
		return
	}
	a.hub.broadcastMembership(ctx, a.dbPool, id)
	a.hub.applyPendingMembership(ctx, a.dbPool, id)
}
