package server

// Sessions start sessions (0.4.0). A running session (the parent) starts, drives and stops other
// sessions (its children) with its owner's approval: on its own desktop daemon, or (0.4.3), for a
// parent running in a cluster pod, as cluster pods (delegation_cluster.go).
//
// The credential is the parent's own session token and nothing else (delegateParent). It is turned
// into an in-process principal of kind sessionDelegateKind (delegatePrincipal) whose visibility is
// narrowed to the parent's own children (canSee). authRunner never yields that kind, so the runner
// contract and /mcp can never act as a delegate; the principal is built only in delegation*.go
// (TestDelegatePrincipalBuiltOnlyInDelegationFiles). Every start goes through startChild, which
// builds the contract request from the allowlisted childStart and never from a caller's body.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	sessionDelegateKind = "session_delegate" // never produced by authRunner

	delegationRequestTTL    = 10 * time.Minute
	delegationSweepEvery    = 5 * time.Minute
	childrenChangedThrottle = 2 * time.Second
	delegationMaxWait       = 25 * time.Second // long-poll cap, like maxAnswerHold

	maxRunningChildrenPerParent  = 3
	maxStartsPerParent           = 20
	maxRunningChildrenPerAccount = 6
	maxStartsPerAccountPerHour   = 60

	delegationPromptExcerpt  = 500      // runes in the event and card
	maxDelegationPromptBytes = 64 << 10 // 413 above this
	maxDelegationTitleRunes  = 200
)

// The children_changed throttle belongs to a later part of the feature; this keeps it referenced
// until then.
var _ = childrenChangedThrottle

var delegationAllowedFields = map[string]bool{
	"engine": true, "model": true, "effort": true, "repo": true, "no_repo": true,
	"runtime": true, "title": true, "prompt": true, "keep_running": true,
}

// The refusals delegateParent and the start path answer with. The CLI shows them as they are.
const (
	msgDelegateDaemonToken  = "use the session's own token"
	msgDelegateBoardToken   = "a board token cannot start sessions"
	msgDelegateOtherSession = "a session's token acts only for its own session"
	msgDelegateEnded        = "the session has ended"
	msgDelegateNoOwner      = "delegation needs a session owned by a person"
	msgDelegateDepth        = "a started session cannot start sessions"
	msgChildrenOnDaemon     = "children run on the parent's desktop daemon"
	msgClusterNotConfigured = "the cluster runtime is not configured"
	msgClusterChildRuntime  = `a cluster parent's children run on the cluster: runtime must be "cluster"`
	msgDelegateDisconnected = "the session's daemon is not connected"
	msgDelegateFields       = "a started session's start carries only the allowlisted fields"
)

// delegateOpts tunes delegateParent for one route. AllowDisconnected admits a parent whose daemon
// is not connected (the returned *DaemonConn is then nil): the child routes that read or stop
// (Task 7) still answer while the laptop sleeps; a start never sets it.
type delegateOpts struct {
	AllowDisconnected bool
}

// delegateParent authenticates a children-route request: the parent's own session token
// (board_tokens row with cap "message" and BoardID == "") or a runtime token, for the path's {id}.
// Refused: the daemon master token (403 "use the session's own token"), a board-scoped (assist)
// token (403 "a board token cannot start sessions"), an agent token or a browser login (401), another
// session's token (403). Then: parent missing → 404; terminal status → 409 "the session has ended";
// SpawningAccountID nil → 403 "delegation needs a session owned by a person"; ParentSessionID set →
// 403 "a started session cannot start sessions"; a cluster parent (onClusterParent) is admitted with
// a nil *DaemonConn while the cluster runtime is configured, else 422 "the cluster runtime is not
// configured"; any other non-workstation session → 422 "children run on the parent's desktop
// daemon"; daemon not connected (a.hub.GetHostDaemon(row.DaemonID)==nil) → 409 "the session's
// daemon is not connected" unless opts.AllowDisconnected. Writes the response itself on failure.
// Logs nothing about the token.
func (a *API) delegateParent(w http.ResponseWriter, r *http.Request, opts delegateOpts) (*db.SessionRow, *DaemonConn, bool) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return nil, nil, false
	}
	raw := a.rawBearer(r)
	if raw == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, nil, false
	}
	if a.isDaemonToken(raw) {
		writeError(w, http.StatusForbidden, msgDelegateDaemonToken)
		return nil, nil, false
	}
	tokenSession, ok := a.delegateTokenSession(w, r, raw)
	if !ok {
		return nil, nil, false
	}
	sid := r.PathValue("id")
	if tokenSession == "" || tokenSession != sid {
		writeError(w, http.StatusForbidden, msgDelegateOtherSession)
		return nil, nil, false
	}
	parent, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", "delegate GetSession", err)
		return nil, nil, false
	}
	if parent == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return nil, nil, false
	}
	if parent.DiscardRequestedAt != nil || terminalSessionStatus(parent.Status) && (!opts.AllowDisconnected || db.TerminallyEnded(parent)) {
		writeError(w, http.StatusConflict, msgDelegateEnded)
		return nil, nil, false
	}
	if derefOrEmpty(parent.SpawningAccountID) == "" {
		writeError(w, http.StatusForbidden, msgDelegateNoOwner)
		return nil, nil, false
	}
	if isChildSession(parent) {
		writeError(w, http.StatusForbidden, msgDelegateDepth)
		return nil, nil, false
	}
	if onClusterParent(parent) {
		if a.hub.JobManager() == nil {
			writeError(w, http.StatusUnprocessableEntity, msgClusterNotConfigured)
			return nil, nil, false
		}
		return parent, nil, true
	}
	if !onDesktopDaemon(parent) {
		writeError(w, http.StatusUnprocessableEntity, msgChildrenOnDaemon)
		return nil, nil, false
	}
	dc := a.hub.GetHostDaemon(parent.DaemonID)
	if dc == nil {
		if a.hub.GetDaemon(parent.DaemonID) != nil {
			// Connected, but not a workstation (a cluster pod's own daemon).
			writeError(w, http.StatusUnprocessableEntity, msgChildrenOnDaemon)
			return nil, nil, false
		}
		if !opts.AllowDisconnected {
			writeError(w, http.StatusConflict, msgDelegateDisconnected)
			return nil, nil, false
		}
	}
	return parent, dc, true
}

// delegateTokenSession resolves a raw session credential to its session: a runtime token, or a
// board_tokens row with no board and the "message" capability (the session's own token). Anything
// else is refused here (the response written), core tokens and the runner key included.
func (a *API) delegateTokenSession(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	if runtime, err := db.ValidateRuntimeToken(r.Context(), a.dbPool, raw); err == nil {
		return runtime.SessionID, true
	}
	row, err := db.ValidateBoardToken(r.Context(), a.dbPool, raw)
	if err != nil {
		if errors.Is(err, db.ErrTokenRevoked) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "session token revoked or expired", "code": sessionTokenRevokedCode,
			})
			return "", false
		}
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	if row.BoardID != "" {
		writeError(w, http.StatusForbidden, msgDelegateBoardToken)
		return "", false
	}
	for _, c := range row.Capabilities {
		if c == "message" {
			a.slideTokenExpiry(r.Context(), row)
			return row.SessionID, true
		}
	}
	writeError(w, http.StatusForbidden, "forbidden")
	return "", false
}

// isChildSession reports whether a session was started by another session (depth 1: a child never
// starts sessions). Any one mark suffices: parent_session_id is SET NULL when the parent row is
// deleted (059), so a kept-running child whose parent is gone is still known by how it was allowed
// and who started it.
func isChildSession(row *db.SessionRow) bool {
	return row != nil && (row.ParentSessionID != nil || row.DelegationVia != "" || row.StartedByKind == "session")
}

// onDesktopDaemon reports whether a session runs on a workstation daemon (docker or the bare host),
// whose children run on that same daemon.
func onDesktopDaemon(row *db.SessionRow) bool {
	if row == nil || row.DaemonID == "" || row.DaemonID == clusterDaemonID {
		return false
	}
	return derefOrEmpty(row.Runtime) != runnerRuntimeCluster
}

// onClusterParent reports whether a session runs in a cluster pod, whose children run as cluster
// pods (0.4.3).
//
// The runtime column alone says so. Only the server writes it (InsertClusterSessionAs, and
// SetSessionPosture for its own desktop launches); a daemon's session_started upsert never touches
// it. daemon_id does not: a cluster session is inserted on the placeholder clusterDaemonID, and its
// pod's session_started moves the row to the pod's own ephemeral daemon id (insertSessionAs), so a
// running cluster session is almost never on clusterDaemonID.
func onClusterParent(row *db.SessionRow) bool {
	return row != nil && derefOrEmpty(row.Runtime) == runnerRuntimeCluster
}

// delegationDaemonID is the daemon a session's delegation rows (rules, grants, requests) are keyed
// on: the stable clusterDaemonID for a cluster session, whatever pod currently carries it (a resume
// brings a new pod with a new id), else the session's own desktop daemon.
func delegationDaemonID(row *db.SessionRow) string {
	if onClusterParent(row) {
		return clusterDaemonID
	}
	return row.DaemonID
}

// delegatePrincipal is the in-process principal a parent acts as. Never built from a request body.
// It carries no liveness proof; delegatePrincipalFor adds a cluster parent's.
func delegatePrincipal(parent *db.SessionRow) runnerPrincipal {
	return runnerPrincipal{Kind: sessionDelegateKind, Sub: parent.ID,
		OnBehalfOf: derefOrEmpty(parent.SpawningAccountID), Aud: coreAuthAudience, Caps: []string{coreAuthRunnerCap}}
}

// delegatePrincipalFor is delegatePrincipal with, for a cluster parent, the owner's liveness proof
// core needs to release the owner's credentials to a child pod (a start, or a resume of an idle
// child by a message): decidedSid (the browser session of the person who just allowed the start)
// when given, else the parent's recorded launch proof (clusterOwnerProof). A desktop parent's
// delegate carries none: its children run on the owner's own workstation.
func (a *API) delegatePrincipalFor(ctx context.Context, parent *db.SessionRow, decidedSid string) runnerPrincipal {
	p := delegatePrincipal(parent)
	if onClusterParent(parent) {
		p.delegateProof = a.clusterOwnerProof(ctx, parent.ID, decidedSid)
	}
	return p
}

// childStart is the allowlisted start (Task 4 parses it from the body; Task 5 from the stored row).
type childStart struct {
	Engine      string `json:"engine"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Repo        string `json:"repo,omitempty"`
	NoRepo      bool   `json:"no_repo,omitempty"`
	Runtime     string `json:"runtime"` // resolved: "docker" | "daemon" | "cluster"
	Title       string `json:"title"`
	Prompt      string `json:"prompt"`
	KeepRunning bool   `json:"keep_running,omitempty"`
}

// startChild starts a child of parent from cs, as the delegate principal, on the parent's own daemon
// (or, for a cluster parent, on the cluster). via is "rule:<id>" | "grant:<id>" | "request:<id>".
// idemKey is passed to StartSession (scope session_delegate:<parent>). It never sets Env,
// CallbackURL, CallbackSecret, GitURL, Provider, AutoStop or Grant.
func (a *API) startChild(ctx context.Context, parent *db.SessionRow, cs childStart, via, idemKey string) (StartResponse, *APIError) {
	return a.startChildScoped(ctx, parent, cs, via, idemKey, "", "")
}

// startChildScoped is startChild with an idempotency scope: "" is the parent's own
// (session_delegate:<parent>); the server's start of a decided request passes
// delegateDecideScope(parent), the only other scope delegateStartProblem admits, and the decider's
// browser session (decidedSid), which a cluster child's credentials are released against.
func (a *API) startChildScoped(ctx context.Context, parent *db.SessionRow, cs childStart, via, idemKey, scope, decidedSid string) (StartResponse, *APIError) {
	if parent == nil || derefOrEmpty(parent.SpawningAccountID) == "" {
		return StartResponse{}, apiErrorf(http.StatusForbidden, "%s", msgDelegateNoOwner)
	}
	if isChildSession(parent) {
		return StartResponse{}, apiErrorf(http.StatusForbidden, "%s", msgDelegateDepth)
	}
	if !onDesktopDaemon(parent) && !onClusterParent(parent) {
		return StartResponse{}, apiErrorf(http.StatusUnprocessableEntity, "%s", msgChildrenOnDaemon)
	}
	req := childRunnerRequest(parent, cs, via)
	req.IdemScope = scope
	return a.StartSession(ctx, a.delegatePrincipalFor(ctx, parent, decidedSid), req, idemKey)
}

// delegateStartProblem is the start path's own check of a delegate's request (defense in depth:
// startChild builds only allowlisted requests). A delegate starts only on its parent's pinned
// desktop daemon, explicitly docker or the host, or on the cluster pinned to the cluster's daemon
// (startClusterChild then checks the parent is a cluster parent), for its own parent, with no
// control channel. Its idempotency scope is its own, or the server's scope for a decided request of
// this parent.
func delegateStartProblem(req runnerStartRequest, p runnerPrincipal) *APIError {
	onCluster := req.Runtime == runnerRuntimeCluster && req.DaemonID == clusterDaemonID
	onDesktop := (req.Runtime == runnerRuntimeDocker || req.Runtime == runnerRuntimeDaemon) && req.DaemonID != "" && req.DaemonID != clusterDaemonID
	if !onCluster && !onDesktop {
		return apiErrorf(http.StatusUnprocessableEntity, "%s", msgChildrenOnDaemon)
	}
	if req.ParentSessionID == "" || req.ParentSessionID != p.Sub || len(req.Env) > 0 || req.CallbackURL != "" ||
		req.CallbackSecret != "" || req.GitURL != "" || req.Provider != "" || req.AutoStop || req.Grant != nil ||
		req.CronID != "" || req.TargetDaemonID != "" || req.NoOperatorFallback ||
		(req.IdemScope != "" && req.IdemScope != delegateDecideScope(p.Sub)) {
		return apiErrorf(http.StatusUnprocessableEntity, "%s", msgDelegateFields)
	}
	return nil
}

// childParentPrivate reads whether a child's parent is private, so the child is inserted private
// (R24: inherited through the insert, never a later MarkPrivate). A parent that cannot be read
// fails the start.
func (a *API) childParentPrivate(ctx context.Context, parentID string) (bool, *APIError) {
	if parentID == "" || a.dbPool == nil {
		return false, nil
	}
	row, err := db.GetSession(ctx, a.dbPool, parentID)
	if err != nil {
		return false, apiErrorf(http.StatusInternalServerError, "could not read the parent session")
	}
	if row == nil {
		return false, apiErrorf(http.StatusNotFound, "session not found")
	}
	return row.Private, nil
}

// parentLabel is the parent's name in a child's "started by": its title, else its label.
func (a *API) parentLabel(ctx context.Context, parentID string) string {
	if a.dbPool == nil {
		return ""
	}
	row, err := db.GetSession(ctx, a.dbPool, parentID)
	if err != nil || row == nil {
		return ""
	}
	return sessionLabel(derefOrEmpty(row.Title), row.Repo, row.ID)
}

// RegisterDelegationRoutes mounts the children routes (filled in by later tasks). They are not
// part of the agent contract: main.go calls this, never RegisterRunnerContractRoutes.
func (a *API) RegisterDelegationRoutes(mux *http.ServeMux) {
	a.registerDelegationChildRoutes(mux)
	mux.HandleFunc("GET /api/sessions/{id}/child-approval", a.HandleChildApproval)
	mux.HandleFunc("PUT /api/sessions/{id}/child-approval", a.HandleChildApproval)
	mux.HandleFunc("GET /api/sessions/{id}/budget", a.HandleGetSessionBudget)
	mux.HandleFunc("POST /api/sessions/{id}/children", a.HandlePostChildren)
	mux.HandleFunc("GET /api/sessions/{id}/children/models", a.HandleGetChildModels)
	mux.HandleFunc("GET /api/delegation-requests/{id}", a.HandleGetDelegationRequest)
	mux.HandleFunc("POST /api/delegation-requests/{id}/expire", a.HandleExpireDelegationRequest)
	mux.HandleFunc("POST /api/delegation-requests/{id}/decide", a.HandleDecideDelegationRequest)
	mux.HandleFunc("GET /api/delegation-rules", a.HandleGetDelegationRules)
	mux.HandleFunc("POST /api/delegation-rules", a.HandlePostDelegationRule)
	mux.HandleFunc("DELETE /api/delegation-rules/{id}", a.HandleDeleteDelegationRule)
	mux.HandleFunc("POST /api/delegation-grants/{id}/revoke", a.HandleRevokeDelegationGrant)
}
