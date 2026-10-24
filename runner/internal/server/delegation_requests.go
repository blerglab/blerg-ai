package server

// Delegation requests after they are stored (spec §2): the parent waits on one (long poll) or
// withdraws it (expire); the owner decides it from the card, with a browser login that carries a
// sid. The decision is a compare-and-set from pending; "session" adds a grant and "always" a rule
// in the same transaction. After an allow the server starts the child itself from the STORED
// request, never from anything in the decide call, and records the outcome on the request and in
// a delegation_decided event. A sweeper expires stale requests.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	msgDelegationNotFound   = "not found"
	msgDelegationNotPending = "the request is no longer pending"
	msgChoiceNotOffered     = "this choice was not offered for this request"
	msgDecideBody           = `the body is {"choice": "once" | "session" | "always" | "deny"}`
	msgStartInterrupted     = "the start was interrupted"
	msgStartFailed          = "the session could not be started"

	// delegationStartStale: an allowed request with no outcome after this long lost its start
	// (the server stopped between the decision and the start).
	delegationStartStale = 2 * time.Minute
	maxDecideBodyBytes   = 4 << 10
)

// delegateDecideScope is the idempotency scope of the server's own start of a decided request
// (keys "dr:"+id). It is not the parent's scope (session_delegate:<parent>), so a key the parent
// chooses can never alias a decided start, and the reverse.
func delegateDecideScope(parentID string) string {
	return sessionDelegateKind + "_decide:" + parentID
}

// delegationCaller is who called a request route: a session (its own token) or a person (a browser
// login, human, acting for itself). Both empty: a credential that is neither.
type delegationCaller struct {
	SessionID string // set for a session token
	Account   string // set for a browser
	Sid       string
}

// delegationCallerOf: session token first (messaging token with BoardID "" or runtime token), else a
// browser token (authBrowser-equivalent without writing). ok=false only when there is no credential
// at all (the handler answers 401); otherwise no error is written here, and the handlers answer the
// uniform 404 for anything not allowed.
func (a *API) delegationCallerOf(r *http.Request) (delegationCaller, bool) {
	raw := a.rawBearer(r)
	if raw == "" {
		return delegationCaller{}, false
	}
	if a.dbPool == nil || a.isDaemonToken(raw) {
		return delegationCaller{}, true
	}
	ctx := r.Context()
	if rt, err := db.ValidateRuntimeToken(ctx, a.dbPool, raw); err == nil {
		return delegationCaller{SessionID: rt.SessionID}, true
	}
	if tok, err := db.ValidateBoardToken(ctx, a.dbPool, raw); err == nil {
		if tok.BoardID != "" {
			return delegationCaller{}, true
		}
		for _, c := range tok.Capabilities {
			if c == "message" {
				a.slideTokenExpiry(ctx, tok)
				return delegationCaller{SessionID: tok.SessionID}, true
			}
		}
		return delegationCaller{}, true
	}
	if p, ok := a.verifyBrowserToken(raw); ok && p.Kind == "human" && p.OnBehalfOf == "" && p.Sub != "" {
		return delegationCaller{Account: p.Sub, Sid: p.Sid}, true
	}
	return delegationCaller{}, true
}

// delegationRequestView is GET /api/delegation-requests/{id}. The owner's browser also gets the
// parent, the offered choices and the stored request (the full prompt).
type delegationRequestView struct {
	RequestID       string          `json:"request_id"`
	Status          string          `json:"status"`
	Choice          string          `json:"choice"`
	ChildSessionID  string          `json:"child_session_id"`
	StartError      string          `json:"start_error"`
	ExpiresAt       time.Time       `json:"expires_at"`
	ParentSessionID string          `json:"parent_session_id,omitempty"`
	Offered         []string        `json:"offered,omitempty"`
	Request         json.RawMessage `json:"request,omitempty"`
}

// delegationDecideReply is the decide route's 200.
type delegationDecideReply struct {
	RequestID      string `json:"request_id"`
	Status         string `json:"status"`
	Choice         string `json:"choice"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	StartError     string `json:"start_error,omitempty"`
}

// delegationConflictReply is the 409 of decide and expire: the request is no longer pending, and
// this is what it is now (with the child an allow started).
type delegationConflictReply struct {
	Error          string `json:"error"`
	RequestID      string `json:"request_id"`
	Status         string `json:"status"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	StartError     string `json:"start_error,omitempty"`
}

// delegationExpireReply is the expire route's 200.
type delegationExpireReply struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

// delegationDecidedPayload is the delegation_decided event in the parent's transcript.
type delegationDecidedPayload struct {
	RequestID      string `json:"request_id"`
	Status         string `json:"status"`
	Choice         string `json:"choice"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	StartError     string `json:"start_error,omitempty"`
}

func conflictReply(r db.DelegationRequest) delegationConflictReply {
	return delegationConflictReply{Error: msgDelegationNotPending, RequestID: r.ID, Status: r.Status,
		ChildSessionID: r.ChildSessionID, StartError: r.StartError}
}

// delegationSettled: the request has its final answer. An allowed request whose start has not
// reported yet (no child, no start error) is not settled: a waiter keeps waiting for the child.
func delegationSettled(r db.DelegationRequest) bool {
	if r.Status == "pending" {
		return false
	}
	return r.Status != "allowed" || r.ChildSessionID != "" || r.StartError != ""
}

// delegationHold is how long a GET holds for ?wait=N: min(N, delegationMaxWait) seconds; 0 when
// N is absent or not a positive number.
func delegationHold(q string) time.Duration {
	n, err := strconv.Atoi(q)
	if err != nil || n <= 0 {
		return 0
	}
	if n >= int(delegationMaxWait/time.Second) {
		return delegationMaxWait
	}
	return time.Duration(n) * time.Second
}

// awaitDelegation re-reads the request until it is settled or hold passes. The channel is taken
// before each read, so a signal between the read and the wait is never missed. Every
// exit releases only this caller's registration without detaching concurrent polls.
func (a *API) awaitDelegation(ctx context.Context, id string, hold time.Duration) (*db.DelegationRequest, error) {
	deadline := time.NewTimer(hold)
	defer deadline.Stop()
	for {
		ch, release := a.delegationWaiters.acquire(id)
		cur, err := db.GetDelegationRequest(ctx, a.dbPool, id)
		if err != nil || cur == nil || delegationSettled(*cur) {
			release()
			return cur, err
		}
		select {
		case <-ch:
			release()
		case <-deadline.C:
			release()
			return db.GetDelegationRequest(ctx, a.dbPool, id)
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
}

// HandleGetDelegationRequest is GET /api/delegation-requests/{id}?wait=N.
func (a *API) HandleGetDelegationRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.delegationCallerOf(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	if a.dbPool == nil || !validSessionID(id) {
		writeError(w, http.StatusNotFound, msgDelegationNotFound)
		return
	}
	ctx := r.Context()
	req, err := db.GetDelegationRequest(ctx, a.dbPool, id)
	if err != nil {
		writeDatabaseFailure(w, "read the request", "delegation GetDelegationRequest", err)
		return
	}
	owner := req != nil && caller.Account != "" && caller.Account == req.AccountID
	parent := req != nil && caller.SessionID != "" && caller.SessionID == req.ParentSessionID
	if !owner && !parent {
		writeError(w, http.StatusNotFound, msgDelegationNotFound)
		return
	}
	if hold := delegationHold(r.URL.Query().Get("wait")); hold > 0 && !delegationSettled(*req) {
		cur, err := a.awaitDelegation(ctx, id, hold)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			writeDatabaseFailure(w, "read the request", "delegation await", err)
			return
		}
		if cur == nil {
			writeError(w, http.StatusNotFound, msgDelegationNotFound)
			return
		}
		req = cur
	}
	view := delegationRequestView{RequestID: req.ID, Status: req.Status, Choice: req.Choice,
		ChildSessionID: req.ChildSessionID, StartError: req.StartError, ExpiresAt: req.ExpiresAt}
	if owner {
		view.ParentSessionID = req.ParentSessionID
		view.Offered = req.Offered
		view.Request = req.Request
	}
	writeJSON(w, http.StatusOK, view)
}

// HandleExpireDelegationRequest is POST /api/delegation-requests/{id}/expire: the parent withdraws
// its own pending request (the CLI's timeout). After an allow it answers 409 with the child; an
// allow whose start is still running is waited for, so the answer names the child.
func (a *API) HandleExpireDelegationRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.delegationCallerOf(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	if a.dbPool == nil || caller.SessionID == "" || !validSessionID(id) {
		writeError(w, http.StatusNotFound, msgDelegationNotFound)
		return
	}
	ctx := r.Context()
	got, err := db.ExpireDelegationRequest(ctx, a.dbPool, id, caller.SessionID)
	switch {
	case errors.Is(err, db.ErrDelegationNotFound):
		writeError(w, http.StatusNotFound, msgDelegationNotFound)
		return
	case errors.Is(err, db.ErrDelegationState):
		if !delegationSettled(got) {
			if cur, werr := a.awaitDelegation(ctx, id, delegationMaxWait); werr == nil && cur != nil {
				got = *cur
			}
		}
		writeJSON(w, http.StatusConflict, conflictReply(got))
		return
	case err != nil:
		writeDatabaseFailure(w, "expire the request", "delegation ExpireDelegationRequest", err)
		return
	}
	log.Printf("delegation: parent %s withdrew request %s", got.ParentSessionID, got.ID)
	a.recordDelegationDecided(context.WithoutCancel(ctx), got)
	a.delegationWaiters.signal(got.ID)
	writeJSON(w, http.StatusOK, delegationExpireReply{RequestID: got.ID, Status: got.Status})
}

// HandleDecideDelegationRequest is POST /api/delegation-requests/{id}/decide {choice}: a person with
// a browser login carrying a sid, the request's owner, and nobody else (the uniform 404). The body
// carries the choice and nothing else: any other field is refused (422), never read.
func (a *API) HandleDecideDelegationRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.delegationCallerOf(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	if a.dbPool == nil || caller.Account == "" || caller.Sid == "" || !validSessionID(id) {
		writeError(w, http.StatusNotFound, msgDelegationNotFound)
		return
	}
	req, err := db.GetDelegationRequest(r.Context(), a.dbPool, id)
	if err != nil {
		writeDatabaseFailure(w, "read the request", "delegation decide ownership", err)
		return
	}
	if req == nil || req.AccountID != caller.Account {
		writeError(w, http.StatusNotFound, msgDelegationNotFound)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDecideBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, msgDecideBody)
		return
	}
	var body struct {
		Choice string `json:"choice"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.Decode(new(any)) != io.EOF {
		// Owner first: a stranger learns nothing from the shape of its body.
		if req, gerr := db.GetDelegationRequest(r.Context(), a.dbPool, id); gerr != nil || req == nil || req.AccountID != caller.Account {
			writeError(w, http.StatusNotFound, msgDelegationNotFound)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, msgDecideBody)
		return
	}
	got, apiErr := a.decideDelegation(r.Context(), caller.Account, caller.Sid, id, body.Choice)
	if apiErr != nil {
		if apiErr.Status == http.StatusConflict {
			if got.ID != "" && !delegationSettled(got) {
				if cur, werr := a.awaitDelegation(r.Context(), id, delegationMaxWait); werr == nil && cur != nil {
					got = *cur
				}
			}
			writeJSON(w, http.StatusConflict, conflictReply(got))
			return
		}
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, delegationDecideReply{RequestID: got.ID, Status: got.Status, Choice: got.Choice,
		ChildSessionID: got.ChildSessionID, StartError: got.StartError})
}

// decideDelegation runs in one transaction: DecideDelegationRequestTx (CAS); "session" →
// InsertDelegationGrantTx{engine, runtime, daemon; repo/no_repo retained for audit from the stored request,
// created_from_request}; "always" → InsertDelegationRuleTx{engine, runtime, daemon_id = request's
// daemon, local_only = engine is hermes|openclaw, created_via "card"}. After commit (never inside the
// tx): for allowed, the server starts the child from the stored request (startDecidedChild) and
// records the outcome with SetDelegationRequestStart; then the delegation_decided event; then the
// waiters are signaled. It pushes nothing. A 409 returns the request as it now is.
func (a *API) decideDelegation(ctx context.Context, account, sid, id, choice string) (db.DelegationRequest, *APIError) {
	status := "allowed"
	switch choice {
	case "once", "session", "always":
	case "deny":
		status = "denied"
	default:
		return db.DelegationRequest{}, apiErrorf(http.StatusUnprocessableEntity, "%s", msgDecideBody)
	}
	notFound := apiErrorf(http.StatusNotFound, "%s", msgDelegationNotFound)
	conflict := apiErrorf(http.StatusConflict, "%s", msgDelegationNotPending)
	cur, err := db.GetDelegationRequest(ctx, a.dbPool, id)
	if err != nil {
		log.Printf("delegation decide %s: %v", id, err)
		return db.DelegationRequest{}, apiErrorf(http.StatusInternalServerError, "could not read the request")
	}
	if cur == nil || cur.AccountID != account {
		return db.DelegationRequest{}, notFound
	}
	if cur.Status != "pending" {
		return *cur, conflict
	}
	if !containsString(cur.Offered, choice) {
		return db.DelegationRequest{}, apiErrorf(http.StatusUnprocessableEntity, "%s", msgChoiceNotOffered)
	}
	var cs childStart // the stored request; its budget line is ignored
	if err := json.Unmarshal(cur.Request, &cs); err != nil {
		log.Printf("delegation decide %s: stored request: %v", id, err)
		return db.DelegationRequest{}, apiErrorf(http.StatusInternalServerError, "could not read the request")
	}
	storedChoice := choice
	if status == "denied" {
		storedChoice = ""
	}
	decided, apiErr := a.decideTx(ctx, *cur, cs, account, sid, status, storedChoice)
	if apiErr != nil {
		if apiErr.Status == http.StatusConflict && decided.Status == "pending" {
			// Pending past its deadline (the sweeper has not run yet): expire it now.
			decided = a.expireOverdue(ctx, decided)
		}
		return decided, apiErr
	}
	log.Printf("delegation: request %s %s (%s) by %s", decided.ID, decided.Status, choice, account)
	bg := context.WithoutCancel(ctx) // the start must not die with the decide call
	if decided.Status == "allowed" {
		child, startErr := a.startDecidedChild(bg, decided, cs)
		decided.ChildSessionID, decided.StartError = child, startErr
	}
	a.recordDelegationDecided(bg, decided)
	a.delegationWaiters.signal(decided.ID)
	return decided, nil
}

// decideTx is the decision's transaction: the CAS and the grant or rule it creates.
func (a *API) decideTx(ctx context.Context, cur db.DelegationRequest, cs childStart, account, sid, status, choice string) (db.DelegationRequest, *APIError) {
	fail := func(what string, err error) (db.DelegationRequest, *APIError) {
		log.Printf("delegation decide %s: %s: %v", cur.ID, what, err)
		return db.DelegationRequest{}, apiErrorf(http.StatusInternalServerError, "could not record the decision")
	}
	if choice == "always" && cur.DaemonID == clusterDaemonID {
		// A rule names its daemon row: the cluster's may not exist yet on an install where no cluster
		// session has run (a desktop parent's first cluster child).
		if err := db.UpsertDaemon(ctx, a.dbPool, clusterDaemonID, "cluster", "runner", ""); err != nil {
			return fail("cluster daemon", err)
		}
	}
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return fail("begin", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if choice == "session" || choice == "always" {
		parent, err := db.LockDelegationParent(ctx, tx, cur.ParentSessionID)
		if err != nil {
			return fail("parent policy", err)
		}
		if decidedParentProblem(parent, cur) != "" || db.TerminallyEnded(parent) || !containsString(offeredChoices(parent, cs), choice) {
			return cur, apiErrorf(http.StatusUnprocessableEntity, "the parent's authorization changed; this choice is no longer offered")
		}
	}
	decided, err := db.DecideDelegationRequestTx(ctx, tx, cur.ID, account, status, choice, account, sid)
	switch {
	case errors.Is(err, db.ErrDelegationNotFound):
		return db.DelegationRequest{}, apiErrorf(http.StatusNotFound, "%s", msgDelegationNotFound)
	case errors.Is(err, db.ErrDelegationState):
		return decided, apiErrorf(http.StatusConflict, "%s", msgDelegationNotPending)
	case err != nil:
		return fail("cas", err)
	}
	switch choice {
	case "session":
		repo := cs.Repo
		if cs.NoRepo {
			repo = ""
		}
		if _, err := db.InsertDelegationGrantTx(ctx, tx, db.DelegationGrant{ParentSessionID: cur.ParentSessionID,
			AccountID: account, Engine: cs.Engine, Runtime: cs.Runtime, DaemonID: cur.DaemonID, Repo: repo,
			NoRepo: cs.NoRepo, CreatedFromRequest: cur.ID}); err != nil {
			return fail("grant", err)
		}
	case "always":
		if _, err := db.InsertDelegationRuleTx(ctx, tx, db.DelegationRule{AccountID: account, Engine: cs.Engine,
			Runtime: cs.Runtime, DaemonID: cur.DaemonID, LocalOnly: defaultLocalOnly(cs.Engine), CreatedVia: "card"}); err != nil {
			return fail("rule", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fail("commit", err)
	}
	return decided, nil
}

// expireOverdue expires a pending request past its deadline and announces it; it returns the row
// as it then is.
func (a *API) expireOverdue(ctx context.Context, r db.DelegationRequest) db.DelegationRequest {
	got, err := db.ExpireDelegationRequest(ctx, a.dbPool, r.ID, r.ParentSessionID)
	if err != nil {
		if errors.Is(err, db.ErrDelegationState) {
			return got
		}
		log.Printf("delegation: expire overdue %s: %v", r.ID, err)
		return r
	}
	a.recordDelegationDecided(context.WithoutCancel(ctx), got)
	a.delegationWaiters.signal(got.ID)
	return got
}

// defaultLocalOnly: Hermes and OpenClaw rules match only while the daemon reports the engine's
// endpoint as local (spec §3).
func defaultLocalOnly(engine string) bool {
	return engine == "hermes" || engine == "openclaw"
}

// decidedParentProblem re-checks the parent at the start of a decided request, with the checks of
// delegateParent: "" when it may start.
func decidedParentProblem(parent *db.SessionRow, r db.DelegationRequest) string {
	switch {
	case parent == nil || parent.DiscardRequestedAt != nil || terminalSessionStatus(parent.Status):
		return msgDelegateEnded
	case parent.DelegationEndedAt != nil && !r.CreatedAt.After(*parent.DelegationEndedAt):
		return msgDelegateEnded
	case derefOrEmpty(parent.SpawningAccountID) == "" || derefOrEmpty(parent.SpawningAccountID) != r.AccountID:
		return msgDelegateNoOwner
	case isChildSession(parent):
		return msgDelegateDepth
	case !onDesktopDaemon(parent) && !onClusterParent(parent) || !requestDaemonOK(parent, r.DaemonID):
		return msgChildrenOnDaemon
	}
	return ""
}

// requestDaemonOK: a request of this parent may be keyed on daemonID: the parent's own
// (delegationDaemonID), or for a desktop parent also the cluster's (its runtime "cluster" children).
func requestDaemonOK(parent *db.SessionRow, daemonID string) bool {
	return daemonID == delegationDaemonID(parent) || onDesktopDaemon(parent) && daemonID == clusterDaemonID
}

// startDecidedChild starts the child of an allowed request from its stored start cs, under the
// account's start lock (the same as an inline start: limits and the claim of the child row are
// serialized per account): the parent is re-read and re-checked, the stored start re-validated
// against the parent's daemon as it is now, and the limits re-checked without counting this
// request twice. It returns the child id, or the reason the start failed (startError).
func (a *API) startDecidedChild(ctx context.Context, r db.DelegationRequest, cs childStart) (child, startError string) {
	outcomeRecorded := false
	record := func() {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), proposalWriteBudget)
		defer cancel()
		err := db.SetDelegationRequestStart(writeCtx, a.dbPool, r.ID, child, startError)
		outcomeRecorded = true
		if errors.Is(err, db.ErrDelegationState) {
			if current, getErr := db.GetDelegationRequest(writeCtx, a.dbPool, r.ID); getErr == nil && current != nil {
				child, startError = current.ChildSessionID, current.StartError
			}
			return
		}
		if err != nil {
			log.Printf("delegation: record start of %s: %v", r.ID, err)
		}
	}
	err := a.withDelegationLock(ctx, r.AccountID, func() (runErr error) {
		current, err := db.GetDelegationRequest(ctx, a.dbPool, r.ID)
		if err != nil {
			return err
		}
		if current == nil {
			return db.ErrDelegationNotFound
		}
		if current.StartRecordedAt != nil || current.ChildSessionID != "" || current.StartError != "" {
			child, startError = current.ChildSessionID, current.StartError
			outcomeRecorded = true
			return nil
		}
		defer func() {
			if runErr != nil {
				startError = msgStartFailed
			}
			record()
		}()
		parent, err := db.GetSession(ctx, a.dbPool, r.ParentSessionID)
		if err != nil {
			return err
		}
		if msg := decidedParentProblem(parent, r); msg != "" {
			startError = msg
			return nil
		}
		// The stored start's runtime must be the one its request is keyed on (the server wrote
		// both; checked again so a row can never move a child between the two sets).
		if (cs.Runtime == runnerRuntimeCluster) != (r.DaemonID == clusterDaemonID) {
			startError = msgChildrenOnDaemon
			return nil
		}
		host := a.parentHost(parent, nil)
		if onClusterParent(parent) || cs.Runtime == runnerRuntimeCluster {
			if a.hub.JobManager() == nil {
				startError = msgClusterNotConfigured
				return nil
			}
			// The decider is the owner, signed in now: their session is the newest proof a later
			// rule or grant start of this parent's cluster children is authorized with.
			recordLaunchAuthSession(ctx, a.dbPool, parent.ID, r.DecidedSid)
		} else if host.dc = a.hub.ownedHostDaemon(ctx, derefOrEmpty(parent.SpawningAccountID), parent.DaemonID); host.dc == nil {
			startError = msgDelegateDisconnected
			return nil
		}
		if msg := a.storedStartProblem(ctx, parent, host, r, cs); msg != "" {
			startError = msg
			return nil
		}
		child, startError = a.startStoredChild(ctx, parent, r, cs)
		return nil
	})
	switch {
	case errors.Is(err, errDelegationBusy):
		startError = msgDelegationBusy
	case err != nil:
		log.Printf("delegation: start of request %s: %v", r.ID, err)
		startError = msgStartFailed
	}
	if !outcomeRecorded {
		record()
	}
	if child != "" {
		log.Printf("delegation: request %s started child %s", r.ID, child)
	} else {
		log.Printf("delegation: request %s did not start", r.ID)
	}
	return child, startError
}

// storedStartProblem re-validates the stored start against the parent's daemon (or the cluster) as
// it is now, and re-checks the limits without counting this request twice: "" when it may start.
func (a *API) storedStartProblem(ctx context.Context, parent *db.SessionRow, host childHost, r db.DelegationRequest, cs childStart) string {
	// The stored start carries the provider the server resolved a bare folder name to; it is not a
	// caller field, so the allowlist re-check reads the start without it (the repo is already
	// owner/name, and validateChildStart below checks the start with its provider).
	recheck := cs
	recheck.Provider = ""
	recheck.WorkspaceLaunch = ""
	if cs.WorkspaceLaunch != "" {
		w, err := db.GetSessionWorkspace(ctx, a.dbPool, parent.ID)
		if err != nil || w == nil || w.Manifest == nil || w.Manifest.LaunchGeneration != cs.WorkspaceLaunch {
			return "parent workspace selection is unavailable or changed"
		}
		host.workspace = w.Manifest
	}
	raw, err := json.Marshal(recheck)
	if err != nil {
		return msgStartFailed
	}
	_, host, apiErr := parseChildStart(raw, host)
	if apiErr != nil {
		return apiErr.Message
	}
	if apiErr := a.validateChildStart(ctx, cs, host, a.delegateFor(ctx, parent, cs.Runtime, r.DecidedSid)); apiErr != nil {
		return apiErr.Message
	}
	if apiErr := a.checkDelegationLimits(ctx, parent, &r); apiErr != nil {
		return apiErr.Message
	}
	return ""
}

// startStoredChild starts the child in the server's own idempotency scope, key "dr:"+id. A cluster
// child's credentials are released against the decider's browser session (for a cluster or a
// desktop parent alike).
func (a *API) startStoredChild(ctx context.Context, parent *db.SessionRow, r db.DelegationRequest, cs childStart) (child, startError string) {
	resp, apiErr := a.startChildScoped(ctx, parent, cs, "request:"+r.ID, "dr:"+r.ID, delegateDecideScope(parent.ID), r.DecidedSid)
	if apiErr != nil {
		return "", apiErr.Message
	}
	return resp.SessionID, ""
}

// recordDelegationDecided writes {request_id, status, choice, child_session_id?, start_error?} as
// kind "delegation_decided", client id delegationEventID(id, "delegation_decided"), in the parent's
// transcript. The id is stable, so a second record of the same request is a no-op.
func (a *API) recordDelegationDecided(ctx context.Context, r db.DelegationRequest) {
	recordServerAgentEvent(ctx, a.hub, a.dbPool, r.ParentSessionID, delegationEventID(r.ID, "delegation_decided"),
		"delegation_decided", delegationDecidedPayload{RequestID: r.ID, Status: r.Status, Choice: r.Choice,
			ChildSessionID: r.ChildSessionID, StartError: r.StartError})
}

// maintainDelegations is one sweep: ExpireStaleDelegationRequests, and allowed requests whose start
// never reported (plan-review #15) get msgStartInterrupted; each row gets its event and wakes its
// waiters. Returns the number of rows changed.
func (a *API) maintainDelegations(ctx context.Context) int {
	if a.dbPool == nil {
		return 0
	}
	n := 0
	expired, err := db.ExpireStaleDelegationRequests(ctx, a.dbPool)
	if err != nil {
		log.Printf("delegation sweep: expire: %v", err)
	}
	stuck, err := db.MarkInterruptedDelegationStarts(ctx, a.dbPool, delegationStartStale, msgStartInterrupted)
	if err != nil {
		log.Printf("delegation sweep: interrupted starts: %v", err)
	}
	for _, r := range append(expired, stuck...) {
		a.recordDelegationDecided(ctx, r)
		a.delegationWaiters.signal(r.ID)
		n++
	}
	if n > 0 {
		log.Printf("delegation sweep: %d expired, %d interrupted", len(expired), len(stuck))
	}
	return n
}

// StartDelegationMaintenance runs the sweep now and every delegationSweepEvery until ctx is done.
// It reports whether it started (there is a database).
func StartDelegationMaintenance(ctx context.Context, api *API) bool {
	if api == nil || api.dbPool == nil {
		return false
	}
	go func() {
		api.maintainDelegations(ctx)
		t := time.NewTicker(delegationSweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				api.maintainDelegations(ctx)
			}
		}
	}()
	return true
}
