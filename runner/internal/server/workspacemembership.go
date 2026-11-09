package server

// Live repository membership (BLG-321, docs/design/workspace-membership.md). The
// server authorizes and publishes; the runtime fences its writers and installs.
// A person's request is authorized on arrival; Main's stays "requested" until the
// person approves that exact proposal. Either way the change waits for a safe turn
// boundary, and nothing about it interrupts a turn, a worker or a shell.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitremote"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	msgMembershipCluster  = "Cluster workspaces can't change repositories yet."
	msgMembershipPath     = "This repository can't be added to this workspace."
	msgMembershipOutside  = "This workspace is a folder. Choose a repository inside it."
	msgMembershipOverlap  = "This repository is already in the workspace, or shares its Git data with one that is."
	msgMembershipChild    = "Only Main can change a workspace's repositories."
	msgMembershipLimit    = "A workspace can hold 20 repositories."
	msgMembershipOne      = "Choose one repository at a time."
	msgMembershipOffline  = "The desktop running this workspace is offline."
	msgMembershipNotFound = "That repository isn't available on this desktop."
	msgMembershipStale    = "The workspace changed. Refresh and try again."
	msgMembershipBusy     = "Another repository change is pending."
	msgMembershipEnded    = "This workspace has ended."
	msgMembershipWorkers  = "Waiting for the workers using this repository to finish."
	msgMembershipDiscard  = "Only the person can discard local files."

	msgMembershipLastRepository = "This is the workspace's last repository."
)

func membershipWaitingText(code string) string {
	switch code {
	case protocol.MembershipWaitTurn:
		return "Waiting for the current turn to finish."
	case protocol.MembershipWaitShell:
		return "Waiting for the open shell to close."
	case protocol.MembershipWaitKeeper:
		return "Waiting for the shell keeper to reconnect. Blerg will retry this repository change."
	case protocol.MembershipWaitKeeperIncompatible:
		return "The shell keeper is from an incompatible Blerg version. Restart the keeper service on this desktop to replace it (this ends its open shells); the repository change continues afterward."
	case protocol.MembershipWaitRuntime:
		return "Waiting for this workspace's runtime to reconnect."
	case protocol.MembershipWaitBusy:
		return "Waiting for another operation to finish."
	case protocol.MembershipWaitFence:
		return "Waiting for a safe moment. Blerg will retry."
	case protocol.MembershipWaitInstall:
		return "The change is saved, but the workspace hasn't reopened. Blerg will retry."
	}
	return "Waiting for a safe moment."
}

func membershipRefusalText(code string) string {
	switch code {
	case protocol.MembershipRefuseSource:
		return "The repository changed or moved after it was chosen."
	case protocol.MembershipRefuseCollision:
		return "A folder with that name already exists in the workspace."
	case protocol.MembershipRefuseQuota:
		return "The workspace has no room for this repository."
	case protocol.MembershipRefuseWork:
		return "The repository has unfinished or undelivered work."
	case protocol.MembershipRefuseLocal:
		return "Git ignores these local files, so they were never delivered. Removing would delete them."
	case protocol.MembershipRefuseAnchor:
		return "A chat is working in this repository. Move it or close it first."
	}
	return "The change could not be prepared. Nothing was changed."
}

type membershipOperationView struct {
	WaitingCode string `json:"waiting_code,omitempty"`
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Phase       string `json:"phase"`
	RequestedBy string `json:"requested_by"`
	MemberID    string `json:"member_id"`
	MemberName  string `json:"member_name"`
	MemberPath  string `json:"member_path,omitempty"`
	Digest      string `json:"proposed_digest"`
	Waiting     string `json:"waiting,omitempty"`
	Refusal     string `json:"refusal,omitempty"`
	// A refused removal's ignored files, and the digest that confirms discarding them.
	LocalFiles  []string `json:"local_files,omitempty"`
	LocalCount  int      `json:"local_count,omitempty"`
	LocalDigest string   `json:"local_digest,omitempty"`
}

type membershipView struct {
	Revision    int64                    `json:"revision"`
	Unsupported string                   `json:"unsupported,omitempty"`
	AddBlocked  string                   `json:"add_blocked,omitempty"`
	Blockers    map[string]string        `json:"blockers"`
	Operation   *membershipOperationView `json:"operation,omitempty"`
	Last        *membershipOperationView `json:"last,omitempty"`
}

func repositoryRequest(op *db.MembershipOperation) *protocol.RepositoryRequest {
	if op == nil || op.RequestedBy != "main" {
		return nil
	}
	if op.Phase != db.MembershipRequested {
		return nil
	}
	return &protocol.RepositoryRequest{ID: op.ID, Kind: op.Kind, MemberName: op.MemberName, Digest: op.Proposed.Digest}
}

func operationView(o *db.MembershipOperation) *membershipOperationView {
	if o == nil {
		return nil
	}
	view := &membershipOperationView{ID: o.ID, Kind: o.Kind, Phase: o.Phase, RequestedBy: o.RequestedBy, MemberID: o.MemberID, MemberName: o.MemberName, Digest: o.Proposed.Digest, Waiting: o.Waiting, Refusal: o.Refusal, LocalFiles: o.LocalFiles.Names, LocalCount: o.LocalFiles.Count, LocalDigest: o.LocalFiles.Digest}
	// The stored reason is the text this function family produced, so the code is
	// recovered from the same source rather than a copy of one sentence.
	for _, code := range []string{protocol.MembershipWaitKeeper, protocol.MembershipWaitKeeperIncompatible} {
		if o.Waiting == membershipWaitingText(code) {
			view.WaitingCode = code
		}
	}
	for _, member := range o.Proposed.Members {
		if member.ID == o.MemberID {
			view.MemberPath = member.Path
		}
	}
	return view
}

func (a *API) RegisterWorkspaceMembershipRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/workspace/repositories", a.handleMembershipRead)
	mux.HandleFunc("POST /api/sessions/{id}/workspace/repositories", a.handleMembershipChange)
	mux.HandleFunc("POST /api/sessions/{id}/workspace/repositories/{op}/decide", a.handleMembershipDecide)
}

type membershipContext struct {
	cloneAddition *protocol.DiscoveryAddition // authenticated clone reply; never client input
	caller        delegationCaller
	row           *db.SessionRow
	m             protocol.WorkspaceManifest
}

// membershipAuthorize requires the live owner or the session's own Main token.
// Every other caller receives the same not found.
func (a *API) membershipAuthorize(r *http.Request, id string) (*membershipContext, *APIError) {
	missing := apiErrorf(http.StatusNotFound, "not found")
	if !validSessionID(id) {
		return nil, missing
	}
	caller, ok := a.delegationCallerOf(r)
	if !ok {
		return nil, apiErrorf(http.StatusUnauthorized, "unauthorized")
	}
	ctx := r.Context()
	row, err := db.GetSession(ctx, a.dbPool, id)
	if err != nil || row == nil || row.SpawningAccountID == nil {
		return nil, missing
	}
	if caller.Account != *row.SpawningAccountID && (caller.SessionID == "" || caller.SessionID != id) {
		return nil, missing
	}
	w, err := db.GetSessionWorkspace(ctx, a.dbPool, id)
	if err != nil || w == nil || w.Manifest == nil || w.Manifest.OwnerID != *row.SpawningAccountID {
		return nil, apiErrorf(http.StatusConflict, "This session has no workspace.")
	}
	return &membershipContext{caller: caller, row: row, m: *w.Manifest}, nil
}

func (a *API) membershipBlock(c *membershipContext) string {
	switch {
	case c.row.EndedAt != nil || c.row.ArchivedAt != nil || c.row.DiscardRequestedAt != nil || c.row.Status == "stopped" || c.row.Status == "ended":
		return msgMembershipEnded
	case c.row.ParentSessionID != nil:
		return msgMembershipChild
	case c.m.Runtime == "cluster":
		return msgMembershipCluster
	}
	return ""
}

func (a *API) handleMembershipRead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, problem := a.membershipAuthorize(r, id)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	ctx := r.Context()
	view := membershipView{Revision: c.m.Revision, Unsupported: a.membershipBlock(c), Blockers: map[string]string{}}
	if view.Unsupported == "" {
		if len(c.m.Members) >= protocol.MaxWorkspaceMembers {
			view.AddBlocked = msgMembershipLimit
		}
		for _, member := range c.m.Members {
			if block := membershipRemovalBlock(c.m, member.ID); block != "" {
				view.Blockers[member.ID] = block
			}
		}
	}
	open, err := db.OpenMembershipOperation(ctx, a.dbPool, id)
	if err != nil {
		writeDatabaseFailure(w, "read the repository change", "membership open "+id, err)
		return
	}
	view.Operation = operationView(open)
	if open == nil {
		last, err := db.LatestMembershipOperation(ctx, a.dbPool, id)
		if err != nil {
			writeDatabaseFailure(w, "read the repository change", "membership latest "+id, err)
			return
		}
		if last != nil && (last.Phase == db.MembershipRefused || last.Phase == db.MembershipDenied) {
			view.Last = operationView(last)
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, view)
}

type membershipChangeBody struct {
	OperationID      string                             `json:"operation_id"`
	Kind             string                             `json:"kind"`
	ExpectedRevision int64                              `json:"expected_revision"`
	Selection        protocol.WorkspaceSelectionRequest `json:"selection"`
	// Repository is Main's request: a local path or a credential-free hosted Git URL.
	Repository string `json:"repository"`
	MemberID   string `json:"member_id"`
	// DiscardLocal confirms the local files a refused removal named. Only the person
	// can send it, and only for the digest the runtime reported.
	DiscardLocal string `json:"discard_local"`
}

func (a *API) handleMembershipChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, problem := a.membershipAuthorize(r, id)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	var body membershipChangeBody
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || !validSessionID(body.OperationID) || (body.Kind != "add" && body.Kind != "remove") {
		writeError(w, http.StatusBadRequest, "invalid repository change")
		return
	}
	if block := a.membershipBlock(c); block != "" {
		writeError(w, http.StatusConflict, block)
		return
	}
	op, problem := a.buildMembership(withCoreProof(r.Context(), a.membershipProof(r, c)), c, body)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	created, fresh, err := db.CreateMembershipOperation(r.Context(), a.dbPool, *op)
	switch {
	case errors.Is(err, db.ErrMembershipBusy):
		writeError(w, http.StatusConflict, msgMembershipBusy)
		return
	case errors.Is(err, db.ErrMembershipStale), errors.Is(err, db.ErrMembershipConflict):
		writeError(w, http.StatusConflict, msgMembershipStale)
		return
	case err != nil:
		writeDatabaseFailure(w, "save the repository change", "membership create "+id, err)
		return
	}
	if fresh {
		a.hub.broadcastMembership(context.WithoutCancel(r.Context()), a.dbPool, id)
		if created.Phase == db.MembershipRequested {
			a.pushForMessage(*c.row, db.MessageRow{Kind: "ask", Body: fmt.Sprintf("Main asks to %s %s", created.Kind, created.MemberName), OwnerOnly: true, NoWait: true}) //nolint:contextcheck // best-effort attention delivery, once per new operation
		}
		a.hub.applyPendingMembership(context.WithoutCancel(r.Context()), a.dbPool, id)
	}
	if current, e := db.GetMembershipOperation(r.Context(), a.dbPool, created.ID); e == nil && current != nil {
		created = current
	}
	writeJSON(w, http.StatusAccepted, operationView(created))
}

func (a *API) membershipProof(r *http.Request, c *membershipContext) coreProof {
	if c.caller.Account == "" {
		return coreProof{}
	}
	if p, ok := a.verifyBrowserToken(a.rawBearer(r)); ok {
		return proofFromPrincipal(p)
	}
	return coreProof{}
}

// buildMembership binds the request to one exact proposal. The browser and Main
// supply references only; identity, path and digest are computed here.
func (a *API) buildMembership(ctx context.Context, c *membershipContext, body membershipChangeBody) (*db.MembershipOperation, *APIError) {
	if body.ExpectedRevision != c.m.Revision {
		return nil, apiErrorf(http.StatusConflict, msgMembershipStale)
	}
	op := &db.MembershipOperation{ID: body.OperationID, SessionID: c.row.ID, Kind: body.Kind, Phase: db.MembershipAuthorized, RequestedBy: "person", Principal: c.caller.Account, ApprovedBy: c.caller.Account, ExpectedRevision: c.m.Revision, OldDigest: c.m.Digest}
	if c.caller.Account == "" {
		op.Phase, op.RequestedBy, op.Principal, op.ApprovedBy = db.MembershipRequested, "main", c.caller.SessionID, ""
	}
	next := c.m
	next.Revision = c.m.Revision + 1
	if body.Kind == "remove" {
		if problem := a.confirmDiscard(ctx, c, op, body); problem != nil {
			return nil, problem
		}
		return a.buildRemoval(c, op, next, body.MemberID)
	}
	if len(c.m.Members) >= protocol.MaxWorkspaceMembers {
		return nil, apiErrorf(http.StatusConflict, msgMembershipLimit)
	}
	refs := body.Selection.Members
	if body.Repository != "" {
		if op.RequestedBy != "main" || len(refs) != 0 {
			return nil, apiErrorf(http.StatusBadRequest, "invalid repository change")
		}
		if c.cloneAddition == nil && (strings.Contains(body.Repository, "://") || (strings.HasPrefix(body.Repository, "git@") && strings.Contains(body.Repository, ":"))) {
			ref, ok := gitremote.ParseAddition(body.Repository)
			if !ok {
				return nil, apiErrorf(http.StatusBadRequest, "This Git URL is not supported. Use a connected Git host and a URL without credentials, options, ports, or queries.")
			}
			ctx = withCoreProof(ctx, a.sessionLaunchProof(ctx, c.row.ID))
			kinds, known := a.userRepos.Kinds(ctx, c.m.OwnerID)
			connected := false
			for _, kind := range kinds {
				connected = connected || kind == ref.Provider
			}
			if !known || !connected {
				return nil, apiErrorf(http.StatusConflict, "Connect this Git host in Settings before adding its repositories.")
			}
			member := protocol.WorkspaceMember{ID: "clone-" + op.ID, Name: ref.Name, Path: ref.Name, SourceIdentity: body.Repository, CommonDirectoryIdentity: body.Repository, Destination: body.Repository}
			path, ok := workspaceselection.AdditionPath(c.m.Members, protocol.DiscoverySource{ID: member.ID, Name: member.Name, Path: member.Path})
			if !ok {
				return nil, apiErrorf(http.StatusConflict, msgMembershipPath)
			}
			member.Path = path
			next.Members = append(append([]protocol.WorkspaceMember{}, c.m.Members...), member)
			next.Kind = "folder"
			next.Digest = workspaceselection.Digest(next)
			if next.Validate() != nil {
				return nil, apiErrorf(http.StatusConflict, msgMembershipPath)
			}
			op.Proposed, op.MemberID, op.MemberName = next, member.ID, gitremote.AdditionName(ref)
			op.Source = protocol.SelectionRef{GitURL: body.Repository}
			return op, nil
		}
		refs = []protocol.SelectionRef{{Path: body.Repository}}
	}
	if op.RequestedBy == "main" && body.Repository == "" {
		return nil, apiErrorf(http.StatusBadRequest, "name a repository")
	}
	if len(refs) != 1 {
		return nil, apiErrorf(http.StatusUnprocessableEntity, msgMembershipOne)
	}
	dc := a.hub.FindDaemonForSession(c.row.ID)
	if dc == nil {
		return nil, apiErrorf(http.StatusConflict, msgMembershipOffline)
	}
	if dc.WorkspaceCapabilities.Membership != protocol.WorkspaceMembershipVersion || !dc.WorkspaceCapabilities.Supports(c.m.Runtime, false) {
		return nil, apiErrorf(http.StatusConflict, "Update Blerg on this desktop to change repositories (reinstall its daemon).")
	}
	res := protocol.WorkspaceDiscoveryResult{OK: true, Addition: c.cloneAddition}
	if c.cloneAddition == nil {
		var err error
		res, err = a.hub.requestWorkspaceDiscovery(ctx, c.m.OwnerID, dc, protocol.DiscoverWorkspace{Operation: "addition", ParentLaunchGeneration: c.m.LaunchGeneration, RootIdentity: body.Selection.RootIdentity, Members: refs, Runtime: c.m.Runtime})
		if err != nil {
			return nil, apiErrorf(http.StatusConflict, msgMembershipOffline)
		}
	}
	if !res.OK || res.Addition == nil {
		switch res.ErrorCode {
		case "outside_folder":
			return nil, apiErrorf(http.StatusConflict, msgMembershipOutside)
		case "overlapping_repositories", "submodule":
			return nil, apiErrorf(http.StatusConflict, msgMembershipOverlap)
		}
		return nil, apiErrorf(http.StatusConflict, msgMembershipNotFound)
	}
	source := res.Addition.Source
	path := res.Addition.PhysicalPath
	if path == "" {
		var ok bool
		if path, ok = workspaceselection.AdditionPath(c.m.Members, source); !ok {
			return nil, apiErrorf(http.StatusConflict, msgMembershipPath)
		}
	}
	member := protocol.WorkspaceMember{ID: source.ID, Name: source.Name, Path: path, SourceIdentity: source.SourceIdentity, CommonDirectoryIdentity: source.CommonDirectoryIdentity, Destination: source.SourceIdentity}
	if source.Origin != nil && a.authorizedHostedKeys(ctx, c.m.OwnerID)[repoKey(source.Origin.Provider, source.Origin.FullName)] {
		member.Provider, member.Project = source.Origin.Provider, source.Origin.FullName
	}
	next.Members = append(append([]protocol.WorkspaceMember{}, c.m.Members...), member)
	if len(next.Members) > 1 {
		next.Kind = "folder"
	}
	next.Digest = workspaceselection.Digest(next)
	if next.Validate() != nil {
		return nil, apiErrorf(http.StatusConflict, msgMembershipPath)
	}
	op.Proposed, op.MemberID, op.MemberName = next, member.ID, member.Name
	op.Source = protocol.SelectionRef{Path: refs[0].Path, SourceIdentity: source.SourceIdentity}
	return op, nil
}

// confirmDiscard binds a person's confirmation to the refusal they read. The runtime
// compares it to the files again, under its fence.
func (a *API) confirmDiscard(ctx context.Context, c *membershipContext, op *db.MembershipOperation, body membershipChangeBody) *APIError {
	if body.DiscardLocal == "" {
		return nil
	}
	if c.caller.Account == "" {
		return apiErrorf(http.StatusForbidden, msgMembershipDiscard)
	}
	last, err := db.LatestMembershipOperation(ctx, a.dbPool, c.row.ID)
	if err != nil || last == nil || last.Phase != db.MembershipRefused || last.MemberID != body.MemberID || last.LocalFiles.Digest != body.DiscardLocal {
		return apiErrorf(http.StatusConflict, msgMembershipStale)
	}
	op.DiscardLocal = body.DiscardLocal
	return nil
}

func (a *API) buildRemoval(c *membershipContext, op *db.MembershipOperation, next protocol.WorkspaceManifest, id string) (*db.MembershipOperation, *APIError) {
	members := []protocol.WorkspaceMember{}
	for _, member := range c.m.Members {
		if member.ID == id {
			op.MemberID, op.MemberName = member.ID, member.Name
			continue
		}
		members = append(members, member)
	}
	if op.MemberID == "" {
		return nil, apiErrorf(http.StatusConflict, msgMembershipStale)
	}
	if block := membershipRemovalBlock(c.m, id); block != "" {
		return nil, apiErrorf(http.StatusConflict, "%s", block)
	}
	if dc := a.hub.FindDaemonForSession(c.row.ID); dc == nil {
		return nil, apiErrorf(http.StatusConflict, msgMembershipOffline)
	} else if dc.WorkspaceCapabilities.Membership != protocol.WorkspaceMembershipVersion {
		return nil, apiErrorf(http.StatusConflict, "Update Blerg on this desktop to change repositories (reinstall its daemon).")
	}
	next.Members = members
	if len(members) == 0 {
		next.Kind = "folder" // ruling 4: only a host workspace may become empty
	}
	next.Digest = workspaceselection.Digest(next)
	if next.Validate() != nil {
		return nil, apiErrorf(http.StatusConflict, msgMembershipLastRepository)
	}
	op.Proposed = next
	return op, nil
}

// membershipRemovalBlock is what the server knows without asking the runtime.
// Work and anchoring in a named folder are proved by the runtime, under its fence.
func membershipRemovalBlock(m protocol.WorkspaceManifest, id string) string {
	if len(m.Members) == 1 && m.Runtime != "host" {
		return msgMembershipLastRepository
	}
	for i, member := range m.Members {
		if member.ID == id && (member.Path == "." || i == 0 && m.Runtime == "sandbox") {
			return membershipRefusalText(protocol.MembershipRefuseAnchor)
		}
	}
	return ""
}

// handleMembershipDecide is the person's approval of Main's exact proposal.
func (a *API) handleMembershipDecide(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, problem := a.membershipAuthorize(r, id)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	if c.caller.Account == "" {
		writeError(w, http.StatusForbidden, "only the person can approve a repository change")
		return
	}
	var body struct {
		Allow          bool   `json:"allow"`
		ProposedDigest string `json:"proposed_digest"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil {
		writeError(w, http.StatusBadRequest, "invalid decision")
		return
	}
	op, err := db.GetMembershipOperation(r.Context(), a.dbPool, r.PathValue("op"))
	if err != nil || op == nil || op.SessionID != id {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if body.Allow && (op.ExpectedRevision != c.m.Revision || op.OldDigest != c.m.Digest) {
		_ = db.RefuseMembershipOperation(r.Context(), a.dbPool, op.ID, "The workspace changed after this was requested.")
		a.hub.broadcastMembership(context.WithoutCancel(r.Context()), a.dbPool, id)
		writeError(w, http.StatusConflict, msgMembershipStale)
		return
	}
	if err := db.DecideMembershipOperation(r.Context(), a.dbPool, op.ID, c.caller.Account, body.ProposedDigest, body.Allow); err != nil {
		writeError(w, http.StatusConflict, msgMembershipStale)
		return
	}
	a.hub.broadcastMembership(context.WithoutCancel(r.Context()), a.dbPool, id)
	if body.Allow && op.Source.GitURL != "" {
		go a.resolveMembershipClone(context.WithoutCancel(r.Context()), id)
	}
	a.hub.applyPendingMembership(context.WithoutCancel(r.Context()), a.dbPool, id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// membershipControlEarly names the calls an owning desktop may make before its
// connection recovered: reading the journal is what lets it recover at all.
func membershipControlEarly(operation string) bool {
	return operation == "membership_status" || operation == "membership_installed" || operation == "membership_report"
}

func (h *Hub) membershipControl(ctx context.Context, pool *pgxpool.Pool, req protocol.LocalWorkspaceControl, reply *protocol.LocalWorkspaceControlResult) error {
	if req.Membership == nil {
		return errWorkspaceUnavailable
	}
	op, err := db.GetMembershipOperation(ctx, pool, req.Membership.OperationID)
	if err != nil {
		return err
	}
	if op == nil || op.SessionID != req.SessionID {
		return errWorkspaceUnavailable
	}
	switch req.Operation {
	case "membership_status":
		reply.MembershipPhase = op.Phase
		reply.Manifest = &op.Proposed
		return nil
	case "membership_installed":
		return db.CompleteMembershipOperation(ctx, pool, op.ID)
	case "membership_report":
		switch {
		case req.Membership.Refusal == protocol.MembershipRefuseLocal && req.Membership.Local != nil:
			return db.RefuseMembershipRemoval(ctx, pool, op.ID, membershipRefusalText(req.Membership.Refusal), boundedLocalFiles(*req.Membership.Local))
		case req.Membership.Refusal != "":
			return db.RefuseMembershipOperation(ctx, pool, op.ID, membershipRefusalText(req.Membership.Refusal))
		case req.Membership.Waiting != "":
			reason := membershipWaitingText(req.Membership.Waiting)
			if err := db.SetMembershipWaiting(ctx, pool, op.ID, reason); err != nil {
				return err
			}
			if op.Waiting != reason {
				h.broadcastMembership(ctx, pool, req.SessionID)
			}
			return nil
		}
	}
	return errWorkspaceUnavailable
}

// boundedLocalFiles keeps file names, which the agent chooses, safe for a row.
func boundedLocalFiles(local protocol.WorkspaceLocalFiles) protocol.WorkspaceLocalFiles {
	names := make([]string, 0, min(len(local.Names), protocol.MaxWorkspaceLocalNames))
	for _, name := range local.Names[:min(len(local.Names), protocol.MaxWorkspaceLocalNames)] {
		name = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, strings.ToValidUTF8(name, ""))
		if runes := []rune(name); len(runes) > 200 {
			name = string(runes[:199]) + "…"
		}
		names = append(names, name)
	}
	local.Names = names
	return local
}

// applyPendingMembership offers the session's authorized change to its runtime.
// It is called at every turn end, on connect and by the sweep; a repeat is safe.
func (h *Hub) applyPendingMembership(ctx context.Context, pool *pgxpool.Pool, id string) {
	op, err := db.OpenMembershipOperation(ctx, pool, id)
	if err != nil || op == nil || op.Phase == db.MembershipRequested || op.Source.GitURL != "" {
		return
	}
	row, err := db.GetSession(ctx, pool, id)
	if err != nil || row == nil {
		return
	}
	wait := func(reason string) {
		if op.Phase == db.MembershipAuthorized && db.SetMembershipWaiting(ctx, pool, op.ID, reason) == nil && op.Waiting != reason {
			h.broadcastMembership(ctx, pool, id)
		}
	}
	if row.EndedAt != nil || row.Status == "stopped" || row.Status == "ended" {
		if op.Phase == db.MembershipAuthorized {
			_ = db.RefuseMembershipOperation(ctx, pool, op.ID, msgMembershipEnded)
		} else if db.CompleteMembershipOperation(ctx, pool, op.ID) == nil {
			h.membershipChanged(ctx, pool, id) // the commit stands; no runtime will reopen
		}
		return
	}
	dc := h.FindDaemonForSession(id)
	if dc == nil {
		wait(membershipWaitingText(protocol.MembershipWaitRuntime))
		return
	}
	if dc.WorkspaceCapabilities.Membership != protocol.WorkspaceMembershipVersion {
		wait("Waiting for Blerg to be updated on this desktop.")
		return
	}
	if op.Phase == db.MembershipAuthorized && row.Status != "idle" {
		wait(membershipWaitingText(protocol.MembershipWaitTurn))
		return
	}
	if op.Phase == db.MembershipAuthorized && op.Kind == "remove" {
		busy, err := db.WorkspaceMemberInUse(ctx, pool, id, op.MemberID)
		if err != nil {
			return
		}
		if busy {
			wait(msgMembershipWorkers)
			return
		}
	}
	raw, err := json.Marshal(protocol.WorkspaceMembershipApply{Type: "workspace_membership_apply", SessionID: id, OperationID: op.ID, Kind: op.Kind, MemberID: op.MemberID, Proposed: op.Proposed, OldDigest: op.OldDigest, Source: op.Source, DiscardLocal: op.DiscardLocal})
	if err == nil {
		h.trySendToDaemon(dc, raw)
	}
}

// membershipChanged follows every journal step: tell browsers, and on the commit
// or the install tell History and Main exactly once.
func (h *Hub) membershipChanged(ctx context.Context, pool *pgxpool.Pool, id string) {
	op, err := db.LatestMembershipOperation(ctx, pool, id)
	if err != nil || op == nil {
		return
	}
	h.broadcastMembership(ctx, pool, id)
	if op.Phase == db.MembershipRefused {
		h.mu.RLock()
		notify := h.membershipNotice
		h.mu.RUnlock()
		if notify != nil {
			notify(id, fmt.Sprintf("Repository change for %q failed: %s", op.MemberName, op.Refusal), "membership-"+op.ID)
		}
		return
	}
	if op.Phase != db.MembershipInstalled {
		return
	}
	verb, past := "added to", "Added"
	if op.Kind == "remove" {
		verb, past = "removed from", "Removed"
	}
	mark, _ := json.Marshal(agent.MarkPayload{ID: "membership-" + op.ID, Kind: "milestone", By: map[string]string{"person": "person", "main": "agent"}[op.RequestedBy], Text: fmt.Sprintf("%s repository %s", past, op.MemberName), At: time.Now().UTC().Format(time.RFC3339), PostedBy: op.Principal})
	if _, _, err := db.AppendAgentEvent(ctx, pool, id, "membership-"+op.ID, "mark", string(protocol.StripNUL(mark))); err != nil {
		log.Printf("membership mark %s: %v", id, err)
	}
	h.mu.RLock()
	notify := h.membershipNotice
	h.mu.RUnlock()
	if notify != nil {
		path := ""
		for _, member := range op.Proposed.Members {
			if member.ID == op.MemberID {
				path = member.Path
			}
		}
		text := fmt.Sprintf("Repository %q was %s this workspace.", op.MemberName, verb)
		if op.Kind == "add" {
			text = fmt.Sprintf("Repository %q was %s this workspace at %q, relative to the workspace folder. Start new workers with it as usual; running workers keep what they have.", op.MemberName, verb, path)
		}
		notify(id, text, "membership-"+op.ID)
	}
}

func (h *Hub) broadcastMembership(ctx context.Context, pool *pgxpool.Pool, id string) {
	broadcastSessionCreated(ctx, h, pool, id)
}

// SetMembershipNoticeHook registers the API's relay to Main (NewAPI).
func (h *Hub) SetMembershipNoticeHook(fn func(sessionID, text, clientID string)) {
	h.mu.Lock()
	h.membershipNotice = fn
	h.mu.Unlock()
}

func (a *API) membershipNotify(id, text, clientID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row, err := db.GetSession(ctx, a.dbPool, id)
	if err != nil || row == nil || db.TerminallyEnded(row) {
		return
	}
	principal := runnerPrincipal{Kind: "human", Sub: derefOrEmpty(row.SpawningAccountID)}
	if dc := a.hub.FindDaemonForSession(id); dc == nil || dc.CanInputReceipts() {
		if problem := a.sendRelay(ctx, principal, id, text, membershipSource, clientID); problem != nil {
			log.Printf("membership notice %s: %s", id, problem.Message)
		}
	} else if problem := a.sendRelay(ctx, principal, id, text, membershipSource); problem != nil {
		log.Printf("membership notice %s: %s", id, problem.Message)
	}
}

const membershipSource = "workspace_membership"

func (a *API) membershipSweep() time.Duration {
	if a.membershipSweepEvery > 0 {
		return a.membershipSweepEvery
	}
	return 15 * time.Second
}

// RunWorkspaceMembership retries pending changes: a shell closing or a repeated
// apply after a lost reply has no event of its own.
func (a *API) RunWorkspaceMembership(ctx context.Context) {
	ticker := time.NewTicker(a.membershipSweep())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids, err := db.OpenMembershipOperations(ctx, a.dbPool)
			if err != nil {
				continue
			}
			for _, id := range ids {
				go a.resolveMembershipClone(ctx, id)
				a.hub.applyPendingMembership(ctx, a.dbPool, id)
			}
		}
	}
}
